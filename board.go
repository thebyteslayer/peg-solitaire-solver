package main

import (
	"math/bits"
)

// Board geometry: an N x N grid with a `cut` x `cut` square removed from each
// of the four corners (a plus/cross shape). Cells are numbered 0..nCells-1 in
// row-major order over the valid cells. Supports up to 128 cells.
var (
	boardN   int
	boardCut int
	nCells   int
	centerID int

	rc2id  [][]int     // (r,c) -> cell id, or -1
	id2r   []int       // cell id -> row
	id2c   []int       // cell id -> col
	nb     [][4]int    // neighbor cell id per direction, or -1
	perms  [][]int     // dihedral symmetry permutations of cell ids
	color1 []int       // (r+c) mod 3
	color2 []int       // (r-c) mod 3
	dist   []int       // Manhattan distance from cell to center
)

var dirDR = [4]int{-1, 1, 0, 0}
var dirDC = [4]int{0, 0, -1, 1}
var dirName = [4]string{"UP", "DOWN", "LEFT", "RIGHT"}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func isValid(n, cut, r, c int) bool {
	if r < 0 || r >= n || c < 0 || c >= n {
		return false
	}
	hi := n - cut
	corner := (r < cut && c < cut) || (r < cut && c >= hi) ||
		(r >= hi && c < cut) || (r >= hi && c >= hi)
	return !corner
}

func initBoard(n, cut int) {
	boardN, boardCut = n, cut
	rc2id = make([][]int, n)
	for r := range rc2id {
		rc2id[r] = make([]int, n)
		for c := range rc2id[r] {
			rc2id[r][c] = -1
		}
	}
	id2r = id2r[:0]
	id2c = id2c[:0]
	id := 0
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			if isValid(n, cut, r, c) {
				rc2id[r][c] = id
				id2r = append(id2r, r)
				id2c = append(id2c, c)
				id++
			}
		}
	}
	nCells = id
	if nCells > 128 {
		panic("board too large (max 128 cells)")
	}
	if n%2 == 0 {
		panic("board side must be odd so a center cell exists")
	}
	centerID = rc2id[n/2][n/2]

	nb = make([][4]int, nCells)
	color1 = make([]int, nCells)
	color2 = make([]int, nCells)
	dist = make([]int, nCells)
	cr, cc := id2r[centerID], id2c[centerID]
	for i := 0; i < nCells; i++ {
		r, c := id2r[i], id2c[i]
		dist[i] = abs(r-cr) + abs(c-cc)
		for d := 0; d < 4; d++ {
			nr, nc := r+dirDR[d], c+dirDC[d]
			if isValid(n, cut, nr, nc) {
				nb[i][d] = rc2id[nr][nc]
			} else {
				nb[i][d] = -1
			}
		}
		color1[i] = ((r+c)%3 + 3) % 3
		color2[i] = ((r-c)%3 + 3) % 3
	}

	// dihedral group D4 transforms (center m = n/2).
	m := n - 1
	type tf func(r, c int) (int, int)
	tfs := []tf{
		func(r, c int) (int, int) { return r, c },
		func(r, c int) (int, int) { return c, m - r },
		func(r, c int) (int, int) { return m - r, m - c },
		func(r, c int) (int, int) { return m - c, r },
		func(r, c int) (int, int) { return r, m - c },
		func(r, c int) (int, int) { return m - r, c },
		func(r, c int) (int, int) { return c, r },
		func(r, c int) (int, int) { return m - c, m - r },
	}
	perms = make([][]int, len(tfs))
	for t := range tfs {
		perms[t] = make([]int, nCells)
		for i := 0; i < nCells; i++ {
			tr, tc := tfs[t](id2r[i], id2c[i])
			perms[t][i] = rc2id[tr][tc]
		}
	}
}

// State is a board bitset (up to 128 cells): bit i set => peg at cell i.
type State struct{ lo, hi uint64 }

func (s State) get(i int) bool {
	if i < 64 {
		return (s.lo>>uint(i))&1 == 1
	}
	return (s.hi>>uint(i-64))&1 == 1
}
func (s *State) set(i int) {
	if i < 64 {
		s.lo |= 1 << uint(i)
	} else {
		s.hi |= 1 << uint(i-64)
	}
}
func (s *State) clr(i int) {
	if i < 64 {
		s.lo &^= 1 << uint(i)
	} else {
		s.hi &^= 1 << uint(i-64)
	}
}
func (s State) count() int { return bits.OnesCount64(s.lo) + bits.OnesCount64(s.hi) }

func less(a, b State) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}

// canon returns the lexicographically smallest of the 8 symmetric variants.
func canon(s State) State {
	best := s
	first := true
	for t := 0; t < 8; t++ {
		var ts State
		p := perms[t]
		lo := s.lo
		for lo != 0 {
			i := bits.TrailingZeros64(lo)
			lo &= lo - 1
			ts.set(p[i])
		}
		hi := s.hi
		for hi != 0 {
			i := bits.TrailingZeros64(hi)
			hi &= hi - 1
			ts.set(p[64+i])
		}
		if first || less(ts, best) {
			best = ts
			first = false
		}
	}
	return best
}

func startState() State {
	var s State
	for i := 0; i < nCells; i++ {
		s.set(i)
	}
	s.clr(centerID) // center starts empty
	return s
}

func goalState() State {
	var s State
	s.set(centerID)
	return s
}

// invClass is the peg-solitaire color invariant (preserved by every jump).
type invClass struct{ a, b, c, d int }

func classOf(s State) invClass {
	var n1, n2 [3]int
	for i := 0; i < nCells; i++ {
		if s.get(i) {
			n1[color1[i]]++
			n2[color2[i]]++
		}
	}
	mod := func(x int) int { return ((x % 2) + 2) % 2 }
	return invClass{mod(n1[0] - n1[1]), mod(n1[1] - n1[2]), mod(n2[0] - n2[1]), mod(n2[1] - n2[2])}
}
