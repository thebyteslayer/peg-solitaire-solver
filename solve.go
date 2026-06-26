package main

import (
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
)

// Move: a peg starts at `from`, makes one or more jumps over adjacent pegs, and
// ends on `to`; each jumped peg is removed.
//
// A straight move (turns == nil) goes in the single direction `dir`: its jumped
// cells are the odd-step cells on the line from `from` to `to`, recomputed on
// demand so no slice is stored — keeping the default solver's hot path
// allocation-free.
//
// A corner move (turns != nil) is the classic peg-solitaire move: one peg making
// consecutive jumps that may turn corners, counted as a single move. `turns`
// lists the direction of each jump; `dir` is the first one (for the arrow) and
// `to` is the final landing. Only the target solver generates these.
type Move struct {
	from  int
	dir   int
	to    int
	turns []int8
}

// oversOf reconstructs the jumped-over cells of m (used off the hot path, e.g.
// the TUI). Hot callers (applyMove, moveDelta) walk inline to avoid allocating.
func oversOf(m Move) []int {
	var ov []int
	if m.turns == nil {
		for cur := m.from; cur != m.to; {
			over := nb[cur][m.dir]
			ov = append(ov, over)
			cur = nb[over][m.dir]
		}
		return ov
	}
	cur := m.from
	for _, d := range m.turns {
		over := nb[cur][d]
		ov = append(ov, over)
		cur = nb[over][d]
	}
	return ov
}

// genMoves returns all legal moves (including each prefix of a multi-jump) from s.
func genMoves(s State, out []Move) []Move {
	out = out[:0]
	for i := 0; i < nCells; i++ {
		if !s.get(i) {
			continue
		}
		for d := 0; d < numDirs; d++ {
			cur := i
			for {
				over := nb[cur][d]
				if over < 0 || !s.get(over) {
					break
				}
				land := nb[over][d]
				if land < 0 || s.get(land) {
					break
				}
				out = append(out, Move{from: i, dir: d, to: land})
				cur = land
			}
		}
	}
	return out
}

// moveDelta is the change in total distance-to-center caused by a move.
func moveDelta(m Move) int {
	d := dist[m.to] - dist[m.from]
	for cur := m.from; cur != m.to; {
		over := nb[cur][m.dir]
		d -= dist[over]
		cur = nb[over][m.dir]
	}
	return d
}

func applyMove(s State, m Move) State {
	s.clr(m.from)
	if m.turns == nil {
		for cur := m.from; cur != m.to; {
			over := nb[cur][m.dir]
			s.clr(over)
			cur = nb[over][m.dir]
		}
	} else {
		cur := m.from
		for _, d := range m.turns {
			over := nb[cur][d]
			s.clr(over)
			cur = nb[over][d]
		}
	}
	s.set(m.to)
	return s
}

var (
	goalSt    State
	maxPegsDB int
	pathMoves []Move

	// Aggregated progress across all workers (read by the TUI status line).
	nodes    int64 // atomic
	minPegs  int64 // atomic
	stopFlag int32 // atomic: set to 1 once any worker has a solution
)

// searcher holds all per-goroutine search state so workers never share mutable
// data (the backward DB is read-only once built, so it is shared safely).
type searcher struct {
	failed *StateSet
	rng    *rand.Rand
	path   []Move // moves on the current DFS branch
	buf    []Move // per-depth move scratch: depth d uses buf[d*moveCap:(d+1)*moveCap]
}

const moveCap = 256

func newSearcher(seed int64) *searcher {
	return &searcher{
		failed: newStateSet(1 << 16),
		rng:    rand.New(rand.NewSource(seed)),
		buf:    make([]Move, (nCells+2)*moveCap),
	}
}

// task is one root subtree to search: explore from `state`, reached via `prefix`.
type task struct {
	state  State
	prefix []Move
}

func solve() bool {
	if targetMoves > 0 {
		return targetSolve()
	}
	return defaultSearch()
}

// defaultSearch runs the fast parallel any-solution search, leaving the solution
// in pathMoves. It builds the backward DB and is also used by the target solver
// to obtain a quick reference solution to expand from.
func defaultSearch() bool {
	goalSt = goalState()
	start := startState()
	buildBackwardDB(goalSt, maxPegsDB)

	pathMoves = nil
	atomic.StoreInt64(&nodes, 0)
	atomic.StoreInt64(&minPegs, int64(nCells))
	atomic.StoreInt32(&stopFlag, 0)

	nw := runtime.NumCPU()
	tasks := buildTasks(start, 8*nw)
	if len(tasks) == 0 {
		return false
	}

	// Hand every root subtree to a buffered channel so the producer never blocks
	// (workers may stop early once a solution is found, leaving tasks unconsumed).
	taskCh := make(chan task, len(tasks))
	for _, t := range tasks {
		taskCh <- t
	}
	close(taskCh)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winner []Move
	for i := 0; i < nw; i++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			w := newSearcher(seed)
			for t := range taskCh {
				if atomic.LoadInt32(&stopFlag) != 0 {
					return
				}
				w.path = w.path[:0]
				if w.dfs(t.state, 0) {
					full := append(append([]Move{}, t.prefix...), w.path...)
					mu.Lock()
					if winner == nil {
						winner = full
					}
					mu.Unlock()
					atomic.StoreInt32(&stopFlag, 1)
					return
				}
			}
		}(int64(i) + 1)
	}
	wg.Wait()

	if winner != nil {
		pathMoves = winner
		return true
	}
	return false
}

// buildTasks expands a breadth-first frontier from start (folding symmetric
// duplicates via canon) until it holds at least `target` distinct states, then
// returns one task per frontier state. Splitting the work this finely lets the
// pool race many disjoint subtrees and stop the instant one of them solves.
func buildTasks(start State, target int) []task {
	type node struct {
		s    State
		path []Move
	}
	seen := map[State]bool{canon(start): true}
	frontier := []node{{start, nil}}
	var buf [moveCap]Move
	for len(frontier) < target {
		var next []node
		for _, nd := range frontier {
			moves := genMoves(nd.s, buf[:0])
			mv := make([]Move, len(moves))
			copy(mv, moves)
			for _, m := range mv {
				ns := applyMove(nd.s, m)
				if ns.count() <= maxPegsDB {
					// Already in endgame territory; keep this node itself as a
					// task rather than descending into the DB-decided region.
					continue
				}
				cf := canon(ns)
				if seen[cf] {
					continue
				}
				seen[cf] = true
				p := make([]Move, len(nd.path)+1)
				copy(p, nd.path)
				p[len(nd.path)] = m
				next = append(next, node{ns, p})
			}
		}
		if len(next) == 0 {
			break // frontier can't grow further; search what we have
		}
		frontier = next
	}
	tasks := make([]task, len(frontier))
	for i, nd := range frontier {
		tasks[i] = task{nd.s, nd.path}
	}
	return tasks
}

// tailSolve walks from a DB state down to the goal, only ever stepping into
// states that remain in the backward DB. Guaranteed to succeed because DB
// membership means the goal is reachable. Read-only on shared state.
func tailSolve(s State) []Move {
	if s == goalSt {
		return nil
	}
	var buf [moveCap]Move
	moves := genMoves(s, buf[:0])
	mv := make([]Move, len(moves))
	copy(mv, moves)
	for _, m := range mv {
		ns := applyMove(s, m)
		if ns != goalSt && !backwardDB.has(canon(ns)) {
			continue
		}
		if tail := tailSolve(ns); tail != nil || ns == goalSt {
			return append([]Move{m}, tail...)
		}
	}
	return nil
}

func (w *searcher) dfs(s State, depth int) bool {
	pc := s.count()
	cf := canon(s)
	// The backward DB is COMPLETE up to maxPegsDB pegs. So once a state has
	// few enough pegs, it is decided immediately: in DB => solvable (meet &
	// finish via tail), not in DB => provably unsolvable (prune).
	if pc <= maxPegsDB {
		if backwardDB.has(cf) {
			w.path = append(w.path, tailSolve(s)...)
			return true
		}
		return false
	}

	n := atomic.AddInt64(&nodes, 1)
	for {
		mp := atomic.LoadInt64(&minPegs)
		if int64(pc) >= mp || atomic.CompareAndSwapInt64(&minPegs, mp, int64(pc)) {
			break
		}
	}
	if n&0x7FFFF == 0 {
		if atomic.LoadInt32(&stopFlag) != 0 {
			return false
		}
		setStatus(fmt.Sprintf("searching — %d positions explored, fewest pegs so far %d",
			n, atomic.LoadInt64(&minPegs)))
	}
	if w.failed.has(cf) {
		return false
	}

	out := w.buf[depth*moveCap : depth*moveCap+moveCap]
	moves := genMoves(s, out[:0])
	if len(moves) == 0 {
		w.failed.add(cf)
		return false
	}
	// Heuristic: a central-game solution funnels pegs toward the middle. Prefer
	// moves that decrease the board's total distance-to-center the most (delta =
	// dist(to) - dist(from) - sum dist(overs)). A small random shuffle breaks
	// ties and keeps workers from retracing identical barren branches.
	nm := len(moves)
	for i := nm - 1; i > 0; i-- {
		j := w.rng.Intn(i + 1)
		moves[i], moves[j] = moves[j], moves[i]
	}
	var dl [moveCap]int
	for i := 0; i < nm; i++ {
		dl[i] = moveDelta(moves[i])
	}
	// Stable insertion sort by delta (typed, no reflection; nm is small).
	for i := 1; i < nm; i++ {
		m, d := moves[i], dl[i]
		j := i - 1
		for j >= 0 && dl[j] > d {
			moves[j+1], dl[j+1] = moves[j], dl[j]
			j--
		}
		moves[j+1], dl[j+1] = m, d
	}

	for i := 0; i < nm; i++ {
		m := moves[i]
		ns := applyMove(s, m)
		w.path = append(w.path, m)
		if w.dfs(ns, depth+1) {
			return true
		}
		w.path = w.path[:len(w.path)-1]
		if atomic.LoadInt32(&stopFlag) != 0 {
			return false
		}
	}
	w.failed.add(cf)
	return false
}
