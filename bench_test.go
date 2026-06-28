package main

import (
	"math/rand"
	"testing"
	"time"
)

// validateSolution replays pathMoves from the start and asserts it reaches the
// goal, simulating each move jump-by-jump so corner moves are checked correctly
// (a later jump may land on a cell vacated earlier in the same move).
func validateSolution(t testing.TB) {
	s := startState()
	for i, m := range pathMoves {
		if !s.get(m.from) {
			t.Fatalf("move %d: from cell %d has no peg", i, m.from)
		}
		dirs := m.turns
		if dirs == nil { // straight move: same direction for each jump
			for k := 0; k < removedCount(m); k++ {
				dirs = append(dirs, int8(m.dir))
			}
		}
		cur := m.from
		s.clr(cur)
		for _, d := range dirs {
			over := nb[cur][d]
			if over < 0 || !s.get(over) {
				t.Fatalf("move %d: no peg to jump over from cell %d dir %d", i, cur, d)
			}
			land := nb[over][d]
			if land < 0 || s.get(land) {
				t.Fatalf("move %d: cannot land at cell %d (off-board or occupied)", i, land)
			}
			s.clr(over)
			cur = land
		}
		if cur != m.to {
			t.Fatalf("move %d: ended at %d but to=%d", i, cur, m.to)
		}
		s.set(cur)
	}
	if s != goalState() {
		t.Fatalf("final state is not the goal (pegs left=%d)", s.count())
	}
}

// Boards must fit the 64-cell bitboard. (9x9/cut2 = 65 cells and 11x11/cut3 = 85
// cells exceed it and are intentionally unsupported in the all-64-bit build.)
var benchBoards = []struct {
	name      string
	n, cut    int
	maxPegsDB int
}{
	{"english_7_2", 7, 2, 10},
	{"big_9_3", 9, 3, 12},
}

func TestSolveAll(t *testing.T) {
	for _, b := range benchBoards {
		t.Run(b.name, func(t *testing.T) {
			initBoard(b.n, b.cut)
			if classOf(startState()) != classOf(goalState()) {
				t.Skipf("%s is provably unsolvable", b.name)
			}
			maxPegsDB = b.maxPegsDB
			t0 := time.Now()
			ok := solve()
			el := time.Since(t0)
			if !ok {
				t.Fatalf("%s: no solution found", b.name)
			}
			validateSolution(t)
			t.Logf("%-12s solved in %-10v  moves=%d nodes=%d", b.name, el.Round(time.Microsecond), len(pathMoves), nodes)
		})
	}
}

func BenchmarkSolve(b *testing.B) {
	for _, bb := range benchBoards {
		b.Run(bb.name, func(b *testing.B) {
			initBoard(bb.n, bb.cut)
			if classOf(startState()) != classOf(goalState()) {
				b.Skipf("unsolvable")
			}
			maxPegsDB = bb.maxPegsDB
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				solve()
			}
		})
	}
}

// refCanon is a straightforward bit-by-bit reference for canon, used only to
// validate the fast table-driven transform (incl. the >64-cell hi-word path).
func refCanon(s State) State {
	best := s
	first := true
	for t := 0; t < nTransforms; t++ {
		var ts State
		p := perms[t]
		for i := 0; i < nCells; i++ {
			if s.get(i) {
				ts.set(p[i])
			}
		}
		if first || less(ts, best) {
			best, first = ts, false
		}
	}
	return best
}

func TestCanonMatchesReference(t *testing.T) {
	rng2 := rand.New(rand.NewSource(42))
	for _, b := range []struct{ n, cut int }{{7, 2}, {9, 3}, {11, 4}} {
		initBoard(b.n, b.cut)
		for iter := 0; iter < 5000; iter++ {
			var s State
			for i := 0; i < nCells; i++ {
				if rng2.Intn(2) == 0 {
					s.set(i)
				}
			}
			if got, want := canon(s), refCanon(s); got != want {
				t.Fatalf("board %dx%d cells=%d: canon mismatch for state %+v: got %+v want %+v",
					b.n, b.cut, nCells, s, got, want)
			}
		}
	}
}
