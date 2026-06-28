package main

import (
	"fmt"
	"math/bits"
	"sync/atomic"
)

// status is a single human-readable progress line, read by the TUI.
var status atomic.Value

func setStatus(s string) { status.Store(s) }
func getStatus() string {
	if v := status.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Reverse (backward) reachability DB.
//
// We enumerate every board position from which the goal (single center peg) is
// reachable, for peg-counts 1..maxPegs. The forward search then only needs to
// reach ANY position in this set, instead of navigating the brutal endgame.
// Because the DB is COMPLETE up to maxPegs pegs, any position with <= maxPegs
// pegs that is NOT in the DB is provably unsolvable -> an instant prune.
//
// A reverse single-jump ("un-jump"): given a peg at `to`, with `over` and
// `from` empty and collinear, produce pegs at `from` and `over` and empty `to`
// (the inverse of a forward jump from->over->to).

var backwardDB *StateSet

func predecessors(s State, out []State) []State {
	out = out[:0]
	for w := uint64(s); w != 0; w &= w - 1 {
		i := bits.TrailingZeros64(w)
		// peg at i == `to`; look for empty `over` then empty `from`.
		for d := 0; d < numDirs; d++ {
			over := nb[i][d]
			if over < 0 || s.get(over) {
				continue
			}
			from := nb[over][d]
			if from < 0 || s.get(from) {
				continue
			}
			p := s
			p.clr(i)
			p.set(over)
			p.set(from)
			out = append(out, p)
		}
	}
	return out
}

func buildBackwardDB(goal State, maxPegs int) {
	backwardDB = newStateSet(1 << 18)
	frontier := []State{goal}
	backwardDB.add(canon(goal))
	levelPegs := goal.count()
	for len(frontier) > 0 && levelPegs < maxPegs {
		var next []State
		var buf [64]State
		for _, s := range frontier {
			for _, p := range predecessors(s, buf[:0]) {
				cf := canon(p)
				if backwardDB.add(cf) {
					next = append(next, p)
				}
			}
		}
		levelPegs++
		setStatus(fmt.Sprintf("building endgame database — %d pegs, %d positions", levelPegs, backwardDB.len()))
		frontier = next
	}
}
