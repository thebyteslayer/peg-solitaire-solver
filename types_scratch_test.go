package main

import (
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

func startStateAt(empty int) State {
	var s State
	for i := 0; i < nCells; i++ {
		s.set(i)
	}
	s.clr(empty)
	return s
}
func clsSingle(b int) invClass { var s State; s.set(b); return classOf(s) }
func solveWithin(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(d):
			atomic.StoreInt32(&stopFlag, 1)
		}
	}()
	ok := defaultSearch()
	close(done)
	return ok
}
func rebuildSymmetry(sp boardSpec, g int) {
	centerID, goalPegID = g, g
	perms = perms[:0]
	for _, tf := range sp.candidateTransforms() {
		p := make([]int, nCells)
		ok := true
		for i := 0; i < nCells; i++ {
			tr, tc := tf(id2r[i], id2c[i])
			if !inBox(tr, tc) || rc2id[tr][tc] < 0 {
				ok = false
				break
			}
			p[i] = rc2id[tr][tc]
		}
		if ok && p[g] == g {
			perms = append(perms, p)
		}
	}
	nTransforms = len(perms)
	buildCanonTables()
}

func TestDiamondFrom13(t *testing.T) {
	sp := boardSpec{TypeDiamond, 5, 0}
	initBoardSpec(sp)
	maxPegsDB = 10
	a := rc2id[1][3] // reference vacancy
	want := classOf(startStateAt(a))
	// candidate goals: parity-OK, sorted by distance to center
	type cand struct{ id, d int }
	var cs []cand
	for g := 0; g < nCells; g++ {
		if g == a || clsSingle(g) != want {
			continue
		}
		cs = append(cs, cand{g, dist[g]})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	for _, c := range cs[:min(6, len(cs))] {
		g := c.id
		rebuildSymmetry(sp, g)
		startEmptyID = a
		fmt.Printf("try empty(1,3)->peg(%d,%d) dist=%d ... ", id2r[g], id2c[g], c.d)
		t0 := time.Now()
		if solveWithin(40 * time.Second) {
			validateSolution(t)
			fmt.Printf("SOLVED %d moves in %v\n", len(pathMoves), time.Since(t0).Round(time.Second))
			return
		}
		fmt.Printf("no (%.0fs)\n", time.Since(t0).Seconds())
	}
	fmt.Println("none of the 6 most-central goals solved in 40s each")
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
