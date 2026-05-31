package main

import (
	"fmt"
	"math/rand"
	"sort"
)

// Move: a peg starts at `from`, jumps in direction `dir` over one or more
// pegs (the `overs`), landing on `to`. Each jumped peg is removed.
type Move struct {
	from int
	dir  int
	to   int
	overs []int
}

// genMoves returns all legal moves (including each prefix of a multi-jump) from s.
func genMoves(s State, out []Move) []Move {
	out = out[:0]
	for i := 0; i < nCells; i++ {
		if !s.get(i) {
			continue
		}
		for d := 0; d < 4; d++ {
			cur := i
			var overs []int
			for {
				over := nb[cur][d]
				if over < 0 || !s.get(over) {
					break
				}
				land := nb[over][d]
				if land < 0 || s.get(land) {
					break
				}
				// extend the chain
				ov := make([]int, len(overs)+1)
				copy(ov, overs)
				ov[len(overs)] = over
				overs = ov
				out = append(out, Move{from: i, dir: d, to: land, overs: overs})
				cur = land
			}
		}
	}
	return out
}

// moveDelta is the change in total distance-to-center caused by a move.
func moveDelta(m Move) int {
	d := dist[m.to] - dist[m.from]
	for _, o := range m.overs {
		d -= dist[o]
	}
	return d
}

func applyMove(s State, m Move) State {
	s.clr(m.from)
	for _, o := range m.overs {
		s.clr(o)
	}
	s.set(m.to)
	return s
}

var rng = rand.New(rand.NewSource(1))

var (
	failed    *StateSet
	goalSt    State
	nodes     int64
	minPegs   int
	maxPegsDB int
	pathMoves []Move
)

func solve() bool {
	start := startState()
	goalSt = goalState()
	buildBackwardDB(goalSt, maxPegsDB)
	failed = newStateSet(1 << 18)
	pathMoves = nil
	minPegs = nCells
	return dfs(start)
}

// tailSolve walks from a DB state down to any `targetPegs`-peg end state,
// only ever stepping into states that remain in the backward DB. Guaranteed
// to succeed because DB membership means such an end state is reachable.
func tailSolve(s State) []Move {
	if s == goalSt {
		return nil
	}
	var buf [256]Move
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

func dfs(s State) bool {
	pc := s.count()
	cf := canon(s)
	// The backward DB is COMPLETE up to maxPegsDB pegs. So once a state has
	// few enough pegs, it is decided immediately: in DB => solvable (meet &
	// finish via tail), not in DB => provably unsolvable (prune).
	if pc <= maxPegsDB {
		if backwardDB.has(cf) {
			tail := tailSolve(s)
			pathMoves = append(pathMoves, tail...)
			return true
		}
		return false
	}
	nodes++
	if pc < minPegs {
		minPegs = pc
	}
	if nodes&0x7FFFF == 0 {
		setStatus(fmt.Sprintf("searching — %d positions explored, fewest pegs so far %d", nodes, minPegs))
	}
	if failed.has(cf) {
		return false
	}

	var buf [256]Move
	moves := genMoves(s, buf[:0])
	if len(moves) == 0 {
		failed.add(cf)
		return false
	}
	// copy because recursion reuses buffers
	mv := make([]Move, len(moves))
	copy(mv, moves)
	// Heuristic: a central-game solution funnels pegs toward the middle. Prefer
	// moves that decrease the board's total distance-to-center the most (delta =
	// dist(to) - dist(from) - sum dist(overs)). A small random jitter breaks ties
	// and keeps repeated runs from retracing the exact same barren branch.
	rng.Shuffle(len(mv), func(i, j int) { mv[i], mv[j] = mv[j], mv[i] })
	sort.SliceStable(mv, func(a, b int) bool {
		return moveDelta(mv[a]) < moveDelta(mv[b])
	})

	for _, m := range mv {
		ns := applyMove(s, m)
		pathMoves = append(pathMoves, m)
		if dfs(ns) {
			return true
		}
		pathMoves = pathMoves[:len(pathMoves)-1]
	}
	failed.add(cf)
	return false
}
