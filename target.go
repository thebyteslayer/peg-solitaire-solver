package main

import (
	"fmt"
	"slices"
	"time"
)

// Target-move-count solver. Enabled by --target-moves N.
//
// A "move" is a single jump, which may chain over several pegs in a straight
// line (a multi-jump) and still counts as one move. This solver looks for a
// solution that uses *exactly* N moves.
//
// Without --no-move-up it then climbs: if the exact-N search does not produce a
// solution within the per-target "move-up timeout", it moves up to N+1, N+2,
// ... Higher counts are far easier to reach, so the climb settles quickly.
//
// With --no-move-up only the exact count N is attempted, with no time limit by
// default (--timeout caps it).
//
// Each exact-count pass is a depth-first dive pruned by a feasibility window on
// the moves still allowed, by the complete backward DB for the endgame, and by a
// per-depth transposition table. (A shared table is what makes this efficient;
// splitting it across parallel workers loses that dedup and runs slower, so the
// search is single-threaded.)

const defaultMoveUpTimeout = 1 * time.Second

var (
	targetMoves   int           // > 0 enables the exact-move-count search, starting here
	noMoveUp      bool          // if set, only the exact targetMoves count is attempted
	timeout       time.Duration // --timeout: caps the --no-move-up search (0 = none)
	timeoutSet    bool          // whether --timeout was given
	moveUpTimeout time.Duration // --move-up-timeout: per-target budget when climbing
)

var (
	tgtVisited  []*StateSet // tgtVisited[g] = canon states already seen at depth g
	tgtBuf      [][]Move    // per-depth move scratch, reused across nodes at depth g
	tgtPath     []Move      // moves on the current DFS branch
	tgtNodes    int64       // positions expanded (status line)
	tgtTarget   int         // target move count of the current pass
	tgtDeadline time.Time   // wall-clock cutoff for the current pass (zero = none)
	tgtTimedOut bool        // set when the current pass hits the deadline
)

// hMoves is an admissible lower bound on the moves still needed: pc-1 pegs must
// be removed, and a single move removes at most nCells-2 of them (a sweep can
// clear everything but the moving peg and its final cell).
func hMoves(pc int) int {
	rem := pc - 1
	if rem <= 0 {
		return 0
	}
	maxPerMove := nCells - 2
	if maxPerMove < 1 {
		maxPerMove = 1
	}
	return (rem + maxPerMove - 1) / maxPerMove
}

// removedCount is how many pegs a move removes (= number of jumps). For a
// straight move it walks the line cell-by-cell rather than using a Manhattan
// formula, which is wrong on the triangular lattice's diagonal directions.
func removedCount(m Move) int {
	if m.turns != nil {
		return len(m.turns)
	}
	n := 0
	for cur := m.from; cur != m.to; n++ {
		cur = nb[nb[cur][m.dir]][m.dir]
	}
	return n
}

// genCornerMoves lists every legal corner move from s: for each peg, every
// prefix of every chain of jumps it could make, where consecutive jumps may
// turn in any direction (the classic single-move-per-peg rule).
func genCornerMoves(s State, out []Move) []Move {
	out = out[:0]
	for i := 0; i < nCells; i++ {
		if s.get(i) {
			out = cornerJumps(s, i, i, nil, out)
		}
	}
	return out
}

// genTick counts cornerJumps calls so the deadline can be checked even inside a
// single explosive generation (one position can spawn a huge corner-move tree on
// a big board, which would otherwise blow past a timeout uninterrupted).
var genTick int

// cornerJumps extends the chain: the moving peg sits at `cur` on the virtual
// board `vs` (the jumps so far already applied), having started at `start` with
// directions `dirs`. It emits a move for each further jump and recurses.
func cornerJumps(vs State, start, cur int, dirs []int8, out []Move) []Move {
	if genTick++; genTick&0xFFFF == 0 && !tgtDeadline.IsZero() && time.Now().After(tgtDeadline) {
		tgtTimedOut = true
	}
	if tgtTimedOut {
		return out
	}
	for d := 0; d < numDirs; d++ {
		over := nb[cur][d]
		if over < 0 || !vs.get(over) {
			continue
		}
		land := nb[over][d]
		if land < 0 || vs.get(land) {
			continue
		}
		nd := make([]int8, len(dirs)+1)
		copy(nd, dirs)
		nd[len(dirs)] = int8(d)
		out = append(out, Move{from: start, dir: int(nd[0]), to: land, turns: nd})
		ns := vs
		ns.clr(cur)
		ns.clr(over)
		ns.set(land)
		out = cornerJumps(ns, start, land, nd, out)
	}
	return out
}

func targetSolve() bool {
	// Get a quick reference solution from the default solver (this also builds
	// the backward DB). Any count from its length up to the all-singles maximum
	// is then reachable for free by splitting multi-jumps, so only counts BELOW
	// the reference length need the (hard) exact corner-move search.
	if !defaultSearch() {
		pathMoves = nil
		return false // board has no solution at all
	}
	lo := targetMoves
	if lo < 1 {
		lo = 1
	}

	// Decompose the reference solution into atomic single jumps, then regroup:
	// any count from minLen (consecutive same-peg jumps maximally chained into
	// corner moves) up to the all-singles maximum is reachable for free. Only
	// counts BELOW minLen need the (hard) exact corner-move search.
	atoms := atomize(append([]Move(nil), pathMoves...))
	minLen := countRuns(atoms)

	start := startState()
	pathMoves = nil
	tgtNodes = 0

	// A solution removes nCells-1 pegs; with at least one peg gone per move it
	// uses at most nCells-2 moves, so no larger count is ever achievable.
	hi := nCells - 2

	// Per-depth move scratch and visited sets, reused across every target pass.
	tgtBuf = make([][]Move, hi+2)
	tgtVisited = make([]*StateSet, hi+2)

	for t := lo; t <= hi; t++ {
		if t >= minLen {
			// Reachable by regrouping the reference jumps — no search needed.
			pathMoves = regroup(atoms, t)
			return true
		}

		tgtTarget = t
		tgtTimedOut = false
		tgtPath = tgtPath[:0]
		for _, set := range tgtVisited {
			if set != nil {
				set.clear()
			}
		}
		if noMoveUp {
			if timeoutSet {
				tgtDeadline = time.Now().Add(timeout)
			} else {
				tgtDeadline = time.Time{} // no limit
			}
		} else {
			tgtDeadline = time.Now().Add(moveUpTimeout) // per-target budget
		}
		setStatus(fmt.Sprintf("searching for an exact %d-move solution (%d positions explored)", t, tgtNodes))

		if exactDFS(start, 0) {
			pathMoves = append([]Move(nil), tgtPath...)
			return true
		}
		if noMoveUp {
			return false // user asked for exactly this count only
		}
		// otherwise (exhausted or timed out) move up to the next count
	}
	return false
}

// atomize breaks every move of sol into its individual single jumps, yielding
// the solution's full sequence of one-peg-at-a-time jumps (length nCells-2).
func atomize(sol []Move) []Move {
	var out []Move
	for _, m := range sol {
		cur := m.from
		dirs := m.turns
		if dirs == nil { // straight move: same direction each jump
			for cur != m.to {
				over := nb[cur][m.dir]
				land := nb[over][m.dir]
				out = append(out, Move{from: cur, dir: m.dir, to: land})
				cur = land
			}
			continue
		}
		for _, d := range dirs {
			over := nb[cur][d]
			land := nb[over][d]
			out = append(out, Move{from: cur, dir: int(d), to: land})
			cur = land
		}
	}
	return out
}

// countRuns is how many corner moves the atomic jumps form when maximally
// chained: a new move starts wherever the moving peg changes (the next jump does
// not begin where the previous one ended). This is the fewest moves reachable by
// regrouping alone.
func countRuns(atoms []Move) int {
	if len(atoms) == 0 {
		return 0
	}
	runs := 1
	for i := 1; i < len(atoms); i++ {
		if atoms[i-1].to != atoms[i].from {
			runs++
		}
	}
	return runs
}

// regroup batches the atomic jumps into exactly `want` moves (want in
// [countRuns(atoms), len(atoms)]) by merging consecutive same-peg jumps into
// corner moves until the move count drops to `want`.
func regroup(atoms []Move, want int) []Move {
	merges := len(atoms) - want // jumps to fold into a preceding move
	var out []Move
	for i := 0; i < len(atoms); {
		from, dir0 := atoms[i].from, atoms[i].dir
		turns := []int8{int8(atoms[i].dir)}
		j := i
		for j+1 < len(atoms) && atoms[j].to == atoms[j+1].from && merges > 0 {
			j++
			turns = append(turns, int8(atoms[j].dir))
			merges--
		}
		if len(turns) == 1 {
			out = append(out, Move{from: from, dir: dir0, to: atoms[j].to})
		} else {
			out = append(out, Move{from: from, dir: dir0, to: atoms[j].to, turns: turns})
		}
		i = j + 1
	}
	return out
}

// exactDFS reports whether the goal can be reached from s — already g moves in —
// in exactly tgtTarget-g further moves, recording the moves in tgtPath if so.
func exactDFS(s State, g int) bool {
	if tgtTimedOut {
		return false
	}
	if s == goalSt {
		return g == tgtTarget // a lone peg admits no further moves
	}
	pc := s.count()
	r := tgtTarget - g // moves still permitted
	if r <= 0 {
		return false
	}
	// Feasibility window: finishing needs at least hMoves(pc) moves and at most
	// pc-1 moves (each move removes between nCells-2 and 1 pegs).
	if hMoves(pc) > r || pc-1 < r {
		return false
	}
	cf := canon(s)
	// The backward DB is COMPLETE up to maxPegsDB pegs: a small position absent
	// from it can never reach the goal, so prune it regardless of budget.
	if pc <= maxPegsDB && !backwardDB.has(cf) {
		return false
	}
	// Per-depth transposition table: a position already explored at this depth
	// this pass cannot yield anything new (same remaining budget).
	set := tgtVisited[g]
	if set == nil {
		set = newStateSet(1 << 12)
		tgtVisited[g] = set
	}
	if !set.add(cf) {
		return false
	}

	tgtNodes++
	if tgtNodes&0xFFFF == 0 { // ~every 65k nodes: keep timeouts responsive
		if !tgtDeadline.IsZero() && time.Now().After(tgtDeadline) {
			tgtTimedOut = true
			return false
		}
		setStatus(fmt.Sprintf("searching for an exact %d-move solution — %d positions explored",
			tgtTarget, tgtNodes))
	}

	moves := genCornerMoves(s, tgtBuf[g][:0])
	tgtBuf[g] = moves // keep the (possibly grown) backing array for reuse
	nm := len(moves)
	// Order toward the removal rate the budget demands: pc-1 pegs must go in r
	// moves, ~(pc-1)/r per move. Trying moves whose chain length is closest to
	// that rate first dives to an exactly-r-move solution soonest, instead of
	// over/undershooting. (Ordering is a speed heuristic only.)
	need := pc - 1
	// Stable: ties (equal rate distance) keep generation order, which the dive
	// relies on; an unstable sort scrambles them and explores far more.
	slices.SortStableFunc(moves, func(a, b Move) int {
		return abs(removedCount(a)*r-need) - abs(removedCount(b)*r-need)
	})

	for i := 0; i < nm; i++ {
		m := moves[i]
		tgtPath = append(tgtPath, m)
		if exactDFS(applyMove(s, m), g+1) {
			return true
		}
		tgtPath = tgtPath[:len(tgtPath)-1]
		if tgtTimedOut {
			return false
		}
	}
	return false
}
