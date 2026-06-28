package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// cell roles for highlighting the current move
const (
	roleNone = iota
	rolePeg
	roleEmpty
	roleLanding // the peg that just landed (move destination)
	roleFrom    // the cell the moving peg started from (shown as a direction arrow)
	roleSkipped // a jumped-over peg that was just removed
	roleVia     // an intermediate landing ("to") the peg passed through, shown as a direction arrow
)

var (
	stPeg     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stEmpty   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stLanding = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stBlue    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stTitle   = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	stDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stWhite   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	stPink    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
)

type tuiModel struct {
	start       State
	calculating bool
	elapsed     time.Duration
	moves       []Move
	snaps       []State // snaps[i] = board after i moves; snaps[0] = start
	idx         int
	rot         int // render rotation in quarter-turns clockwise (0..3)
	w, h        int // terminal size

	// animation state
	anim       bool         // an animation is currently playing
	animFrames []animFrame  // frames of the segment being played
	animPos    int          // index of the frame currently shown
	animEndIdx int          // move index to settle on when the segment finishes
	animChain  bool         // keep going with the next move when this one ends
	animDelay  time.Duration // delay between animation frames
}

// animFrame is one still of a move animation: a board plus the cell holding the
// white "animation peg" (white < 0 means the peg is mid-jump and not drawn).
type animFrame struct {
	board State
	white int
}

// rotateCW returns g rotated 90 degrees clockwise. Off-board cells stay < 0.
func rotateCW(g [][]int) [][]int {
	rows, cols := len(g), len(g[0])
	out := make([][]int, cols)
	for nr := 0; nr < cols; nr++ {
		out[nr] = make([]int, rows)
		for nc := 0; nc < rows; nc++ {
			out[nr][nc] = g[rows-1-nc][nr]
		}
	}
	return out
}

// rotatedGrid returns the cell-id grid rotated by rot quarter-turns clockwise.
func rotatedGrid(rot int) [][]int {
	g := rc2id
	for i := 0; i < ((rot%4)+4)%4; i++ {
		g = rotateCW(g)
	}
	return g
}

// arrowCW maps each direction glyph to the glyph one quarter-turn clockwise.
var arrowCW = map[string]string{
	"↑": "→", "→": "↓", "↓": "←", "←": "↑",
	"↖": "↗", "↗": "↘", "↘": "↙", "↙": "↖",
}

// rotateArrow rotates a direction glyph by rot quarter-turns clockwise so the
// arrows stay consistent with the rotated board.
func rotateArrow(glyph string, rot int) string {
	for i := 0; i < ((rot%4)+4)%4; i++ {
		if g, ok := arrowCW[glyph]; ok {
			glyph = g
		}
	}
	return glyph
}

// viaGlyph returns the glyph for an intermediate landing entered going dirIn and
// left going dirOut: a straight arrow when the heading is unchanged, otherwise a
// diagonal (↖↗↘↙) pointing the way the peg travels through the corner — the sum
// of the incoming and outgoing headings. These rotate with the board like the
// straight arrows do. dirIn/dirOut are direction indices already rotated to
// match the board.
func viaGlyph(dirIn, dirOut int) string {
	if dirIn == dirOut {
		return dirArrow[dirIn] // straight pass-through
	}
	dr := dirDR[dirIn] + dirDR[dirOut]
	dc := dirDC[dirIn] + dirDC[dirOut]
	switch {
	case dr < 0 && dc < 0:
		return "↖"
	case dr < 0 && dc > 0:
		return "↗"
	case dr > 0 && dc > 0:
		return "↘"
	default: // dr > 0 && dc < 0
		return "↙"
	}
}

// animationDelay is the wait between animation frames; set via --animation-delay.
var animationDelay = 500 * time.Millisecond

// messages
type solvedMsg struct {
	moves   []Move
	elapsed time.Duration
}
type tickMsg struct{}
type animTickMsg struct{}

func animTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return animTickMsg{} })
}

// moveHops reconstructs a move as its sequence of (jumped-over, landing) cells,
// expanding straight multi-jumps as well as corner sweeps.
func moveHops(mv Move) (from int, hops [][2]int) {
	from = mv.from
	cur := mv.from
	dirs := mv.turns
	if dirs == nil {
		for cur != mv.to {
			over := nb[cur][mv.dir]
			land := nb[over][mv.dir]
			hops = append(hops, [2]int{over, land})
			cur = land
		}
		return
	}
	for _, d := range dirs {
		over := nb[cur][int(d)]
		land := nb[over][int(d)]
		hops = append(hops, [2]int{over, land})
		cur = land
	}
	return
}

// buildFrames produces the still frames that animate move moveIndex. The first
// frame highlights the peg about to move; then each jump is one frame in which
// the peg leaves its cell, the jumped-over peg is captured, and the peg lands on
// the next cell (drawn as the white peg). The board is kept exact in every frame
// so nothing lingers or appears out of place — the final frame equals the board
// after the move (snaps[moveIndex+1]).
func (m tuiModel) buildFrames(moveIndex int) []animFrame {
	from, hops := moveHops(m.moves[moveIndex])
	board := m.snaps[moveIndex]
	frames := []animFrame{{board, from}} // the peg about to move, highlighted white
	cur := from
	for _, h := range hops {
		over, land := h[0], h[1]
		board.clr(cur)  // the peg leaves its current cell
		board.clr(over) // capture the jumped-over peg as the peg passes it
		board.set(land) // the peg lands on the next cell
		frames = append(frames, animFrame{board, land})
		cur = land
	}
	return frames
}

// beginAnim arms an animation segment for move moveIndex. reverse plays it
// backwards (used to step to the previous move); endIdx is where idx settles
// when the segment ends; chain keeps animating subsequent moves to the end.
func (m tuiModel) beginAnim(moveIndex int, reverse bool, endIdx int, chain bool) tuiModel {
	frames := m.buildFrames(moveIndex)
	if reverse {
		for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
			frames[i], frames[j] = frames[j], frames[i]
		}
	}
	m.anim = true
	m.animFrames = frames
	m.animPos = 0
	m.animEndIdx = endIdx
	m.animChain = chain
	return m
}

func newTUIModel(start State) tuiModel {
	return tuiModel{start: start, calculating: true, animDelay: animationDelay}
}

// newSolvedTUIModel builds a model whose solution is already known, so the TUI
// opens straight into the replay with no "Calculating" phase.
func newSolvedTUIModel(start State, moves []Move, elapsed time.Duration) tuiModel {
	m := tuiModel{start: start, calculating: false, moves: moves, elapsed: elapsed, animDelay: animationDelay}
	m.snaps = make([]State, len(moves)+1)
	m.snaps[0] = start
	s := start
	for i, mv := range moves {
		s = applyMove(s, mv)
		m.snaps[i+1] = s
	}
	return m
}

func solveCmd() tea.Msg {
	t0 := time.Now()
	solve()
	return solvedMsg{moves: append([]Move(nil), pathMoves...), elapsed: time.Since(t0)}
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m tuiModel) Init() tea.Cmd {
	if m.calculating {
		return tea.Batch(solveCmd, tickCmd())
	}
	return nil // solution already computed; go straight to replay
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		if m.calculating {
			return m, tickCmd() // keep refreshing the status line
		}
		return m, nil
	case animTickMsg:
		if !m.anim {
			return m, nil
		}
		m.animPos++
		if m.animPos < len(m.animFrames) {
			return m, animTickCmd(m.animDelay) // advance within the current move
		}
		// Segment finished: settle on its end move and either chain into the
		// next move (play-to-end) or drop back to the move-preview view.
		m.idx = m.animEndIdx
		if m.animChain && m.idx < len(m.moves) {
			m = m.beginAnim(m.idx, false, m.idx+1, true)
			return m, animTickCmd(m.animDelay)
		}
		m.anim = false
		return m, nil
	case solvedMsg:
		m.calculating = false
		m.moves = msg.moves
		m.elapsed = msg.elapsed
		m.snaps = make([]State, len(m.moves)+1)
		m.snaps[0] = m.start
		s := m.start
		for i, mv := range m.moves {
			s = applyMove(s, mv)
			m.snaps[i+1] = s
		}
		return m, nil
	}
	if m.calculating {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "q", "ctrl+c", "esc":
				return m, tea.Quit
			}
		}
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		k := key.String()
		// Quitting and stopping work at any time.
		switch k {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "backspace": // stop the animation, staying on the current move
			m.anim = false
			m.animChain = false
			return m, nil
		}
		// While an animation plays, ignore everything else so it isn't disturbed.
		if m.anim {
			return m, nil
		}
		switch k {
		case "right", " ":
			if m.idx < len(m.moves) {
				m.idx++
			}
		case "left":
			if m.idx > 0 {
				m.idx--
			}
		case "tab":
			m.rot = (m.rot + 1) % 4 // rotate render clockwise
		case "shift+tab":
			m.rot = (m.rot + 3) % 4 // rotate render counter-clockwise
		case "f", "up":
			m.idx = 0
		case "l", "down":
			m.idx = len(m.moves)
		case "enter": // animate from the current move through to the end
			if m.idx < len(m.moves) {
				m = m.beginAnim(m.idx, false, m.idx+1, true)
				return m, animTickCmd(m.animDelay)
			}
		case ",": // animate just the next move
			if m.idx < len(m.moves) {
				m = m.beginAnim(m.idx, false, m.idx+1, false)
				return m, animTickCmd(m.animDelay)
			}
		case "<", "shift+,": // animate the previous move (played in reverse)
			if m.idx > 0 {
				m = m.beginAnim(m.idx-1, true, m.idx-1, false)
				return m, animTickCmd(m.animDelay)
			}
		case ".": // animate the current move again, in place
			if m.idx > 0 {
				m = m.beginAnim(m.idx-1, false, m.idx, false)
				return m, animTickCmd(m.animDelay)
			}
		}
	}
	return m, nil
}

func (m tuiModel) viewCalculating() string {
	line := getStatus()
	if line == "" {
		line = "starting up"
	}
	content := lipgloss.JoinVertical(lipgloss.Center,
		stPink.Render("Calculating moves"),
		"",
		stDim.Render(line),
	)
	if m.w <= 0 || m.h <= 0 {
		return content
	}
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, content)
}

// renderGrid lays out the board (respecting rotation and the triangular indent),
// asking glyph for the already-styled rune to draw at each on-board cell id.
func (m tuiModel) renderGrid(glyph func(id int) string) string {
	disp := rotatedGrid(m.rot)
	var grid strings.Builder
	for r := 0; r < len(disp); r++ {
		if triLayout && m.rot == 0 {
			// indent each row half a cell so the rows form a centered triangle
			grid.WriteString(strings.Repeat(" ", boardRows-1-r))
		}
		for c := 0; c < len(disp[r]); c++ {
			id := disp[r][c]
			if id < 0 {
				grid.WriteString("  ")
				continue
			}
			grid.WriteString(glyph(id) + " ")
		}
		grid.WriteByte('\n')
	}
	return strings.TrimRight(grid.String(), "\n")
}

func (m tuiModel) View() string {
	if m.calculating {
		return m.viewCalculating()
	}
	if m.anim {
		return m.animView()
	}
	state := m.snaps[m.idx]

	// classify cells for highlighting based on the move that produced this frame
	role := make([]int, nCells)
	for i := 0; i < nCells; i++ {
		if state.get(i) {
			role[i] = rolePeg
		} else {
			role[i] = roleEmpty
		}
	}
	// viaIn/viaOut hold, for each intermediate landing, the heading the peg
	// arrived on and the heading it left on, so we can draw a straight arrow for
	// a pass-through or a curved arrow for a corner turn.
	viaIn := make([]int, nCells)
	viaOut := make([]int, nCells)
	for i := range viaIn {
		viaIn[i], viaOut[i] = -1, -1
	}
	var desc string
	if m.idx > 0 {
		mv := m.moves[m.idx-1]
		role[mv.from] = roleFrom
		for _, o := range oversOf(mv) {
			role[o] = roleSkipped
		}
		// Build the per-jump direction list (expanding straight multi-jumps),
		// then mark each intermediate landing with its in/out heading.
		dirs := mv.turns
		if dirs == nil {
			cur := mv.from
			for cur != mv.to {
				dirs = append(dirs, int8(mv.dir))
				cur = nb[nb[cur][mv.dir]][mv.dir]
			}
		}
		cur := mv.from
		for i, d := range dirs {
			land := nb[nb[cur][int(d)]][int(d)]
			if land != mv.to && i+1 < len(dirs) {
				role[land] = roleVia
				viaIn[land] = int(d)
				viaOut[land] = int(dirs[i+1])
			}
			cur = land
		}
		role[mv.to] = roleLanding
		desc = describeMove(mv, m.rot)
	} else {
		desc = "Start position"
	}

	// board grid (rotated for display only; cell ids/roles are unchanged)
	board := m.renderGrid(func(id int) string {
		switch role[id] {
		case roleEmpty:
			return stEmpty.Render("·")
		case roleLanding:
			return stLanding.Render("●") // destination
		case roleFrom:
			return stBlue.Render(rotateArrow(dirArrow[m.moves[m.idx-1].dir], m.rot)) // the peg that moved: blue arrow
		case roleVia:
			// straight or curved arrow depending on whether the peg turned here
			return stBlue.Render(viaGlyph(rotateDir(viaIn[id], m.rot), rotateDir(viaOut[id], m.rot)))
		case roleSkipped:
			return stBlue.Render("·") // skipped/removed peg: blue .
		default: // rolePeg
			return stPeg.Render("●")
		}
	})

	// header / progress
	pegs := state.count()
	title := stTitle.Render("Peg Solitaire")
	step := fmt.Sprintf("Move %d / %d", m.idx, len(m.moves))
	prog := progressBar(m.idx, len(m.moves), 24)

	var moveLine string
	if m.idx > 0 {
		moveLine = desc // already styled per-token by describeMove
	} else {
		moveLine = stDim.Render(desc)
	}

	header := lipgloss.JoinHorizontal(lipgloss.Center, title, stDim.Render("   "+step))
	body := lipgloss.JoinVertical(lipgloss.Center,
		header, prog, "", board, "", stDim.Render(fmt.Sprintf("pegs left: %d", pegs)))

	legend := "  " + stBlue.Render("→") + stDim.Render(" moved peg   ") +
		stBlue.Render("●") + stDim.Render(" landed   ") +
		stBlue.Render("·") + stDim.Render(" removed   ") +
		stPeg.Render("●") + stDim.Render(" peg   ") +
		stEmpty.Render("·") + stDim.Render(" empty")

	content := lipgloss.JoinVertical(lipgloss.Center,
		body, "", moveLine, legend, "", navHelp(), animHelp())

	// Fill the entire terminal with the content centered (no border).
	if m.w <= 0 || m.h <= 0 {
		return content
	}
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, content)
}

func navHelp() string {
	return stKey.Render("←") + stDim.Render(" prev   ") +
		stKey.Render("→/space") + stDim.Render(" next   ") +
		stKey.Render("↑/f") + stDim.Render(" first   ") +
		stKey.Render("↓/l") + stDim.Render(" last   ") +
		stKey.Render("tab/⇧tab") + stDim.Render(" rotate   ") +
		stKey.Render("q") + stDim.Render(" quit")
}

func animHelp() string {
	return stKey.Render("⏎") + stDim.Render(" play→end   ") +
		stKey.Render(",") + stDim.Render(" step   ") +
		stKey.Render("⇧,") + stDim.Render(" back   ") +
		stKey.Render(".") + stDim.Render(" replay   ") +
		stKey.Render("⌫") + stDim.Render(" stop")
}

// animView renders the current animation frame: a plain peg/empty board with the
// white animation peg, plus the usual chrome and a "playing" status line.
func (m tuiModel) animView() string {
	fr := m.animFrames[m.animPos]
	board := m.renderGrid(func(id int) string {
		switch {
		case id == fr.white:
			return stLanding.Render("●") // the white animation peg
		case fr.board.get(id):
			return stPeg.Render("●")
		default:
			return stEmpty.Render("·")
		}
	})

	pegs := fr.board.count()
	title := stTitle.Render("Peg Solitaire")
	step := fmt.Sprintf("Move %d / %d", m.animEndIdx, len(m.moves))
	prog := progressBar(m.animEndIdx, len(m.moves), 24)

	header := lipgloss.JoinHorizontal(lipgloss.Center, title, stDim.Render("   "+step))
	body := lipgloss.JoinVertical(lipgloss.Center,
		header, prog, "", board, "", stDim.Render(fmt.Sprintf("pegs left: %d", pegs)))
	status := stPink.Render("▶ animating")

	content := lipgloss.JoinVertical(lipgloss.Center, body, "", status, "", animHelp())
	if m.w <= 0 || m.h <= 0 {
		return content
	}
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, content)
}

func progressBar(cur, total, width int) string {
	if total == 0 {
		total = 1
	}
	filled := cur * width / total
	return stLanding.Render(strings.Repeat("█", filled)) +
		stEmpty.Render(strings.Repeat("░", width-filled))
}
