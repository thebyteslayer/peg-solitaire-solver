package main

import (
	"fmt"
	"math/bits"
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
// the TUI). The hot search never calls this — it works in precomputed jump masks.
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

// jump is a fully precomputed straight move (a single jump or a chain of jumps
// in one direction, counted as one move). Every per-cell geometry decision is
// baked in at board-build time so the hot search touches only bitboard words:
//
//   - legal in state s  <=>  (s & needSet) == needSet  &&  (s & needClear) == 0
//     (needSet = the jumped-over pegs; needClear = the cells landed on, which
//     must all be empty — including the intermediate ones a chain passes through)
//   - apply             <=>  s ^ applyMask
//     (applyMask = from | jumped-overs | final-landing: XOR clears the source and
//     every captured peg and lights the destination in one instruction)
//   - delta is the move's change in total distance-to-center, precomputed for the
//     ordering heuristic so no walk is needed per node.
//
// Pointers to these (immutable, board-global) structs are what the search shuffles
// and sorts, so the move-scratch buffers are 8-byte words, not 48-byte Moves.
type jump struct {
	needSet   State
	needClear State
	applyMask State
	delta     int16
	from      uint8
	dir       uint8
	to        uint8
}

// jumpsFrom[i] lists every straight move whose moving peg starts on cell i,
// ordered by direction then increasing chain length.
var jumpsFrom [][]jump

// buildJumpTables enumerates every straight move on the board once. Requires nb
// and dist to be populated, so initBoardSpec calls it last.
func buildJumpTables() {
	jumpsFrom = make([][]jump, nCells)
	for i := 0; i < nCells; i++ {
		for d := 0; d < numDirs; d++ {
			var needSet, needClear State
			distSum := 0 // sum of dist over the captured pegs so far
			cur := i
			for {
				over := nb[cur][d]
				if over < 0 {
					break
				}
				land := nb[over][d]
				if land < 0 {
					break
				}
				needSet.set(over)
				needClear.set(land)
				distSum += dist[over]
				applyMask := needSet // overs...
				applyMask.set(i)     // ...plus the source...
				applyMask.set(land)  // ...plus the destination
				jumpsFrom[i] = append(jumpsFrom[i], jump{
					needSet:   needSet,
					needClear: needClear,
					applyMask: applyMask,
					delta:     int16(dist[land] - dist[i] - distSum),
					from:      uint8(i),
					dir:       uint8(d),
					to:        uint8(land),
				})
				cur = land
			}
		}
	}
}

// genJumps appends a *jump for every legal straight move from s. It walks only
// the set bits (pegs) of the bitboard and tests each candidate with two bitwise
// ops — no geometry walk, no allocation. The emitted pointers index the global
// jump table, so they stay valid no matter how `out` is later reused.
func genJumps(s State, out []*jump) []*jump {
	out = out[:0]
	for w := uint64(s); w != 0; w &= w - 1 {
		js := jumpsFrom[bits.TrailingZeros64(w)]
		for k := range js {
			j := &js[k]
			if (s&j.needSet) == j.needSet && (s&j.needClear) == 0 {
				out = append(out, j)
			}
		}
	}
	return out
}

// moveOf converts a hot-path jump back into the public Move type (used only when
// recording a solution, never inside the search).
func moveOf(j *jump) Move { return Move{from: int(j.from), dir: int(j.dir), to: int(j.to)} }

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
// data (the backward DB is read-only once built, so it is shared safely). Node
// and min-peg counters are kept *local* and flushed to the shared atomics only
// at status checkpoints — writing the globals every node would bounce their
// cache lines between cores and throttle parallel scaling.
type searcher struct {
	failed     *StateSet
	rng        uint64  // xorshift64 state (cheaper than math/rand in the shuffle)
	path       []Move  // moves on the current DFS branch (public form, for output)
	buf        []*jump // per-depth scratch: depth d uses buf[d*moveCap:(d+1)*moveCap]
	localNodes int64   // positions expanded by this worker (flushed in batches)
	localMin   int     // fewest pegs this worker has seen
}

const (
	moveCap   = 256
	nodeBatch = 1 << 19 // flush local node count to the global every this many
)

func newSearcher(seed int64) *searcher {
	// splitmix64 the seed so the xorshift state starts well mixed and nonzero.
	z := uint64(seed) + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	if z == 0 {
		z = 1
	}
	return &searcher{
		failed:   newStateSet(1 << 16),
		rng:      z,
		buf:      make([]*jump, (nCells+2)*moveCap),
		localMin: nCells,
	}
}

// rngn returns a fast pseudo-random int in [0,n) for move-order tie-breaking.
// Exact uniformity is irrelevant here (it only diversifies workers), so a plain
// modulo on the xorshift output is fine and far cheaper than math/rand.
func (w *searcher) rngn(n int) int {
	x := w.rng
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	w.rng = x
	return int(x % uint64(n))
}

// flushMin publishes this worker's fewest-pegs-seen into the shared minimum.
func (w *searcher) flushMin() {
	for {
		mp := atomic.LoadInt64(&minPegs)
		if int64(w.localMin) >= mp || atomic.CompareAndSwapInt64(&minPegs, mp, int64(w.localMin)) {
			return
		}
	}
}

// flushTail publishes the node count left over since the last batch checkpoint
// (plus the latest min) so the shared totals are exact once the worker exits.
func (w *searcher) flushTail() {
	atomic.AddInt64(&nodes, w.localNodes&(nodeBatch-1))
	w.flushMin()
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
			defer w.flushTail() // publish this worker's leftover node/min counts
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
	var buf [moveCap]*jump
	for len(frontier) < target {
		var next []node
		for _, nd := range frontier {
			for _, j := range genJumps(nd.s, buf[:0]) {
				ns := nd.s ^ j.applyMask
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
				p[len(nd.path)] = moveOf(j)
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
	var buf [moveCap]*jump
	for _, j := range genJumps(s, buf[:0]) {
		ns := s ^ j.applyMask
		if ns != goalSt && !backwardDB.has(canon(ns)) {
			continue
		}
		if tail := tailSolve(ns); tail != nil || ns == goalSt {
			return append([]Move{moveOf(j)}, tail...)
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

	w.localNodes++
	if pc < w.localMin {
		w.localMin = pc
	}
	if w.localNodes&(nodeBatch-1) == 0 {
		// Periodic checkpoint: publish this batch to the shared counters (so the
		// TUI sees progress) and check the global stop flag. Doing this once per
		// batch instead of per node keeps the hot path off the shared cache lines.
		atomic.AddInt64(&nodes, nodeBatch)
		w.flushMin()
		if atomic.LoadInt32(&stopFlag) != 0 {
			return false
		}
		setStatus(fmt.Sprintf("searching — %d positions explored, fewest pegs so far %d",
			atomic.LoadInt64(&nodes), atomic.LoadInt64(&minPegs)))
	}
	if w.failed.has(cf) {
		return false
	}

	out := w.buf[depth*moveCap : depth*moveCap+moveCap]
	moves := genJumps(s, out[:0])
	if len(moves) == 0 {
		w.failed.add(cf)
		return false
	}
	// Heuristic: a central-game solution funnels pegs toward the middle. Prefer
	// moves that decrease the board's total distance-to-center the most (delta is
	// precomputed in the jump). A small random shuffle breaks ties and keeps
	// workers from retracing identical barren branches.
	nm := len(moves)
	for i := nm - 1; i > 0; i-- {
		j := w.rngn(i + 1)
		moves[i], moves[j] = moves[j], moves[i]
	}
	// Stable insertion sort by precomputed delta (typed, no reflection; nm small).
	for i := 1; i < nm; i++ {
		m := moves[i]
		j := i - 1
		for j >= 0 && moves[j].delta > m.delta {
			moves[j+1] = moves[j]
			j--
		}
		moves[j+1] = m
	}

	for i := 0; i < nm; i++ {
		j := moves[i]
		ns := s ^ j.applyMask
		w.path = append(w.path, moveOf(j))
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
