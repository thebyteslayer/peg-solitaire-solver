package main

import (
	"math/bits"
)

// Board geometry: an N x N grid with a `cut` x `cut` square removed from each
// of the four corners (a plus/cross shape). Cells are numbered 0..nCells-1 in
// row-major order over the valid cells. Supports up to 128 cells.
var (
	boardType   BoardType
	boardRows   int // bounding-box height (cell grid is iterated row-major)
	boardCols   int // bounding-box width
	boardCut    int
	nCells      int
	numDirs     int  // 4 on the square lattice, 6 on the triangular one
	nTransforms int  // size of this board's symmetry group (<= 8)
	centerID    int  // the board's center cell (the goal cell; symmetry fixes it)
	startEmptyID int // the cell that starts empty
	goalPegID    int // the cell the single surviving peg must finish on
	triLayout   bool // render rows indented to form a triangle
	hasColorInv bool // mod-3 square coloring parity check applies (square only)

	rc2id  [][]int  // (r,c) -> cell id, or -1
	id2r   []int    // cell id -> row
	id2c   []int    // cell id -> col
	nb     [][6]int // neighbor cell id per direction (numDirs used), or -1
	perms  [][]int  // symmetry permutations of cell ids (board-preserving only)
	color1 []int    // (r+c) mod 3
	color2 []int    // (r-c) mod 3
	dist   []int    // distance from cell to center (move-ordering heuristic)

	dirDR    []int    // row delta per direction
	dirDC    []int    // col delta per direction
	dirName  []string // human name per direction
	dirArrow []string // arrow glyph per direction
)

// BoardType selects the board geometry. Each type has its own size parameters.
type BoardType int

const (
	TypeSymmetrical BoardType = iota // N x N minus cut x cut square corners (cross)
	TypeEuropean                     // N x N minus diagonal triangle corners (octagon)
	TypeAsymmetrical                 // symmetrical, but top row + right column dropped
	TypeDiamond                      // cells within Manhattan radius of center
	TypeTriangular                   // triangular lattice (6 neighbors), size = rows
)

var boardTypeNames = []string{"symmetrical", "european", "asymmetrical", "diamond", "triangular"}

func (t BoardType) String() string { return boardTypeNames[t] }

func parseBoardType(s string) (BoardType, bool) {
	for i, n := range boardTypeNames {
		if s == n {
			return BoardType(i), true
		}
	}
	return 0, false
}

// boardSpec fully describes a board: its type and its size parameters. `cut` is
// type-specific (square corner for symmetrical/asymmetrical, triangle leg for
// european/triangular, unused for diamond).
type boardSpec struct {
	typ  BoardType
	size int
	cut  int
}

func defaultSpec(t BoardType) boardSpec {
	switch t {
	case TypeDiamond:
		return boardSpec{t, 5, 0} // radius 5 (counts the center) -> the 41-hole diamond
	case TypeTriangular:
		return boardSpec{t, 5, 0} // the classic 15-hole triangle
	default:
		return boardSpec{t, 7, 2} // the English / European 7x7 boards
	}
}

// boundingN is the side of the (square) grid the board's cells live in.
func (sp boardSpec) boundingN() int {
	if sp.typ == TypeDiamond {
		// `size` is the radius counting the center, so the Manhattan radius is
		// size-1 and the diamond spans 2*(size-1)+1 cells across.
		return 2*sp.size - 1
	}
	return sp.size
}

// centerRC is the (row, col) of the start-empty / goal-peg cell. It is the
// center cell for every type except european, whose central game is unsolvable
// — there the standard target is the cell one row above center.
func (sp boardSpec) centerRC() (int, int) {
	switch sp.typ {
	case TypeDiamond:
		m := sp.size - 1
		return m, m
	case TypeEuropean:
		return sp.size/2 - 1, sp.size / 2
	default:
		return sp.size / 2, sp.size / 2
	}
}

// validate returns a usage message describing why the spec is malformed, or ""
// if it is well-formed.
func (sp boardSpec) validate() string {
	n, cut := sp.size, sp.cut
	switch sp.typ {
	case TypeSymmetrical, TypeEuropean, TypeAsymmetrical:
		if n%2 == 0 || n < 3 || cut < 1 || 2*cut >= n {
			return "size must be odd and >= 3, with 0 < cut < size/2"
		}
	case TypeDiamond:
		if n < 2 {
			return "radius must be >= 2 (counts the center; cut is unused for diamond)"
		}
	case TypeTriangular:
		if n < 2 || cut < 0 || 3*cut >= n {
			return "size (rows) must be >= 2, with 0 <= cut < size/3"
		}
	}
	return ""
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// cellValid reports whether (r,c) is a hole on this board.
func (sp boardSpec) cellValid(r, c int) bool {
	n, cut := sp.size, sp.cut
	if r < 0 || r >= boardRows || c < 0 || c >= boardCols {
		return false
	}
	switch sp.typ {
	case TypeSymmetrical:
		return !squareCorner(n, cut, r, c)
	case TypeEuropean:
		return !triCorner(n, cut, r, c)
	case TypeAsymmetrical:
		// The symmetrical cross, but with the top row and right column dropped:
		// two adjacent sides each one shorter, which breaks the square symmetry.
		if r == 0 || c == n-1 {
			return false
		}
		return !squareCorner(n, cut, r, c)
	case TypeDiamond:
		m := n - 1 // Manhattan radius (size counts the center piece)
		return abs(r-m)+abs(c-m) <= m
	case TypeTriangular:
		if c > r {
			return false // lower-triangular: row r holds columns 0..r
		}
		// Remove a triangle of leg `cut` at each of the three corners, expressed
		// in barycentric coordinates (i,j,k) with i+j+k = n-1.
		i, j, k := c, r-c, (n-1)-r
		return i < n-cut && j < n-cut && k < n-cut
	}
	return false
}

// squareCorner reports whether (r,c) lies in one of the cut x cut square corners.
func squareCorner(n, cut, r, c int) bool {
	hi := n - cut
	return (r < cut && c < cut) || (r < cut && c >= hi) ||
		(r >= hi && c < cut) || (r >= hi && c >= hi)
}

// triCorner reports whether (r,c) lies in one of the four diagonal (staircase)
// corner triangles of leg `cut` — the European board's octagonal trim.
func triCorner(n, cut, r, c int) bool {
	return r+c < cut || r+(n-1-c) < cut || (n-1-r)+c < cut || (n-1-r)+(n-1-c) < cut
}

// directions returns the per-direction deltas, names and arrow glyphs for the
// board's lattice (4-way square, or 6-way triangular).
func (sp boardSpec) directions() (dr, dc []int, names, arrows []string) {
	if sp.typ == TypeTriangular {
		// LEFT, RIGHT, UP-LEFT, UP-RIGHT, DOWN-LEFT, DOWN-RIGHT on the lattice
		// where row r widens to r+1 cells (down-right increments both r and c).
		return []int{0, 0, -1, -1, 1, 1},
			[]int{-1, 1, -1, 0, 0, 1},
			[]string{"LEFT", "RIGHT", "UP-LEFT", "UP-RIGHT", "DOWN-LEFT", "DOWN-RIGHT"},
			[]string{"←", "→", "↖", "↗", "↙", "↘"}
	}
	return []int{-1, 1, 0, 0}, []int{0, 0, -1, 1},
		[]string{"UP", "DOWN", "LEFT", "RIGHT"},
		[]string{"↑", "↓", "←", "→"}
}

// candidateTransforms lists every symmetry of the board's lattice (identity
// first). initBoardSpec keeps only those that map this particular board onto
// itself, so the kept set is the board's actual symmetry group.
func (sp boardSpec) candidateTransforms() []func(r, c int) (int, int) {
	if sp.typ == TypeTriangular {
		n := sp.size
		// Each symmetry permutes the barycentric triple (i,j,k); convert back
		// with r = i+j, c = i.
		tri := func(perm func(i, j, k int) (int, int, int)) func(int, int) (int, int) {
			return func(r, c int) (int, int) {
				i, j, k := c, r-c, (n-1)-r
				i2, j2, _ := perm(i, j, k)
				return i2 + j2, i2
			}
		}
		return []func(int, int) (int, int){
			tri(func(i, j, k int) (int, int, int) { return i, j, k }), // identity
			tri(func(i, j, k int) (int, int, int) { return j, k, i }), // rotate 120
			tri(func(i, j, k int) (int, int, int) { return k, i, j }), // rotate 240
			tri(func(i, j, k int) (int, int, int) { return i, k, j }), // reflections
			tri(func(i, j, k int) (int, int, int) { return k, j, i }),
			tri(func(i, j, k int) (int, int, int) { return j, i, k }),
		}
	}
	m := sp.size - 1
	return []func(int, int) (int, int){
		func(r, c int) (int, int) { return r, c },         // identity
		func(r, c int) (int, int) { return c, m - r },     // rotate 90
		func(r, c int) (int, int) { return m - r, m - c }, // rotate 180
		func(r, c int) (int, int) { return m - c, r },     // rotate 270
		func(r, c int) (int, int) { return r, m - c },     // reflections
		func(r, c int) (int, int) { return m - r, c },
		func(r, c int) (int, int) { return c, r },
		func(r, c int) (int, int) { return m - c, m - r },
	}
}

func inBox(r, c int) bool { return r >= 0 && r < boardRows && c >= 0 && c < boardCols }

// initBoard builds the classic symmetrical board. Kept for the tests and as a
// thin wrapper over the generic builder.
func initBoard(n, cut int) { initBoardSpec(boardSpec{TypeSymmetrical, n, cut}) }

func initBoardSpec(sp boardSpec) {
	boardType = sp.typ
	boardCut = sp.cut
	boardRows, boardCols = sp.boundingN(), sp.boundingN()
	triLayout = sp.typ == TypeTriangular
	hasColorInv = sp.typ != TypeTriangular

	dirDR, dirDC, dirName, dirArrow = sp.directions()
	numDirs = len(dirDR)

	// Enumerate valid cells in row-major order.
	rc2id = make([][]int, boardRows)
	for r := range rc2id {
		rc2id[r] = make([]int, boardCols)
		for c := range rc2id[r] {
			rc2id[r][c] = -1
		}
	}
	id2r = id2r[:0]
	id2c = id2c[:0]
	id := 0
	for r := 0; r < boardRows; r++ {
		for c := 0; c < boardCols; c++ {
			if sp.cellValid(r, c) {
				rc2id[r][c] = id
				id2r = append(id2r, r)
				id2c = append(id2c, c)
				id++
			}
		}
	}
	nCells = id
	if nCells == 0 {
		panic("board has no cells")
	}
	if nCells > 128 {
		panic("board too large (max 128 cells)")
	}

	// Neighbors.
	nb = make([][6]int, nCells)
	for i := 0; i < nCells; i++ {
		r, c := id2r[i], id2c[i]
		for d := 0; d < 6; d++ {
			nb[i][d] = -1
		}
		for d := 0; d < numDirs; d++ {
			nr, nc := r+dirDR[d], c+dirDC[d]
			if inBox(nr, nc) && rc2id[nr][nc] >= 0 {
				nb[i][d] = rc2id[nr][nc]
			}
		}
	}

	// Center: the start-empty / goal-peg cell for this board type.
	cr, cc := sp.centerRC()
	if !inBox(cr, cc) || rc2id[cr][cc] < 0 {
		panic("center cell is not on the board")
	}
	centerID = rc2id[cr][cc]
	// By default the game vacates the center and finishes on it (the complement
	// game). The european board's center game is provably unsolvable (it cannot
	// be reduced to one peg from the center at all), so there the peg finishes
	// one cell above center and the vacancy sits one cell above that — a
	// configuration that is solvable and respects the board's vertical mirror.
	startEmptyID, goalPegID = centerID, centerID
	if sp.typ == TypeEuropean {
		startEmptyID = rc2id[id2r[centerID]-1][id2c[centerID]]
	}

	// Symmetry group: keep only the lattice symmetries that map this board onto
	// itself AND fix the center cell. Fixing the center is essential — the whole
	// search (start, goal, backward DB) is defined relative to that one cell, so
	// a symmetry that moved it would let canonicalization conflate the real goal
	// with rotated copies and report false solutions (matters for boards whose
	// center is off the geometric center, e.g. european).
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
		if ok && p[centerID] == centerID {
			perms = append(perms, p)
		}
	}
	nTransforms = len(perms)
	buildCanonTables()

	// Distance-to-center heuristic.
	dist = make([]int, nCells)
	if sp.typ == TypeTriangular {
		bfsDist(centerID, dist) // lattice distance (Manhattan is wrong when sheared)
	} else {
		cr, cc := id2r[centerID], id2c[centerID]
		for i := 0; i < nCells; i++ {
			dist[i] = abs(id2r[i]-cr) + abs(id2c[i]-cc)
		}
	}

	// Mod-3 color invariant (square lattice only; the triangular lattice has a
	// different theory, so its parity pre-check is skipped).
	color1 = make([]int, nCells)
	color2 = make([]int, nCells)
	if hasColorInv {
		for i := 0; i < nCells; i++ {
			r, c := id2r[i], id2c[i]
			color1[i] = ((r+c)%3 + 3) % 3
			color2[i] = ((r-c)%3 + 3) % 3
		}
	}
}

// bfsDist fills out[i] with the lattice (edge-count) distance from src.
func bfsDist(src int, out []int) {
	for i := range out {
		out[i] = nCells // unreachable sentinel (shouldn't occur on a connected board)
	}
	out[src] = 0
	q := []int{src}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		for d := 0; d < numDirs; d++ {
			if nx := nb[cur][d]; nx >= 0 && out[nx] == nCells {
				out[nx] = out[cur] + 1
				q = append(q, nx)
			}
		}
	}
}

// permTab[t][bytePos][byteVal] is the contribution to the t-transformed state
// of the 8 cells covered by byte `bytePos` when those cells hold the peg pattern
// `byteVal`. canon() then transforms a whole state with 8 (or 16) table lookups
// and ORs instead of touching every set bit individually — the single biggest
// hot-path win, since canon dominates the search.
var permTab [8][16][256]State

func buildCanonTables() {
	for t := 0; t < nTransforms; t++ {
		p := perms[t]
		for bp := 0; bp < 16; bp++ {
			base := bp * 8
			for v := 0; v < 256; v++ {
				var st State
				for k := 0; k < 8; k++ {
					cell := base + k
					if v&(1<<uint(k)) != 0 && cell < nCells {
						st.set(p[cell])
					}
				}
				permTab[t][bp][v] = st
			}
		}
	}
}

// transform applies symmetry t to s via the precomputed byte tables.
func transform(tb *[16][256]State, s State) State {
	var r State
	w := s.lo
	for bp := 0; bp < 8; bp++ {
		e := &tb[bp][byte(w)]
		r.lo |= e.lo
		r.hi |= e.hi
		w >>= 8
	}
	if s.hi != 0 { // boards with > 64 cells only
		w = s.hi
		for bp := 8; bp < 16; bp++ {
			e := &tb[bp][byte(w)]
			r.lo |= e.lo
			r.hi |= e.hi
			w >>= 8
		}
	}
	return r
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
// Transform 0 is the identity, so we seed best with s and skip it.
func canon(s State) State {
	best := s
	for t := 1; t < nTransforms; t++ {
		ts := transform(&permTab[t], s)
		if less(ts, best) {
			best = ts
		}
	}
	return best
}

func startState() State {
	var s State
	for i := 0; i < nCells; i++ {
		s.set(i)
	}
	s.clr(startEmptyID)
	return s
}

func goalState() State {
	var s State
	s.set(goalPegID)
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
