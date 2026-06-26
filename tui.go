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
)

var (
	stPeg     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stEmpty   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stLanding = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stBlue    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stTitle   = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	stDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	stPink    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
)

type tuiModel struct {
	start       State
	calculating bool
	elapsed     time.Duration
	moves       []Move
	snaps       []State // snaps[i] = board after i moves; snaps[0] = start
	idx         int
	w, h        int // terminal size
}

// messages
type solvedMsg struct {
	moves   []Move
	elapsed time.Duration
}
type tickMsg struct{}

func newTUIModel(start State) tuiModel {
	return tuiModel{start: start, calculating: true}
}

// newSolvedTUIModel builds a model whose solution is already known, so the TUI
// opens straight into the replay with no "Calculating" phase.
func newSolvedTUIModel(start State, moves []Move, elapsed time.Duration) tuiModel {
	m := tuiModel{start: start, calculating: false, moves: moves, elapsed: elapsed}
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
		switch key.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "right", " ":
			if m.idx < len(m.moves) {
				m.idx++
			}
		case "left", "tab":
			if m.idx > 0 {
				m.idx--
			}
		case "f", "up":
			m.idx = 0
		case "l", "down":
			m.idx = len(m.moves)
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

func (m tuiModel) View() string {
	if m.calculating {
		return m.viewCalculating()
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
	var arrow, desc string
	if m.idx > 0 {
		mv := m.moves[m.idx-1]
		role[mv.to] = roleLanding
		role[mv.from] = roleFrom
		for _, o := range oversOf(mv) {
			role[o] = roleSkipped
		}
		arrow = dirArrow[mv.dir]
		desc = describeMove(mv)
	} else {
		desc = "Start position"
	}

	// board grid
	var grid strings.Builder
	for r := 0; r < boardRows; r++ {
		if triLayout {
			// indent each row half a cell so the rows form a centered triangle
			grid.WriteString(strings.Repeat(" ", boardRows-1-r))
		}
		for c := 0; c < boardCols; c++ {
			id := rc2id[r][c]
			if id < 0 {
				grid.WriteString("  ")
				continue
			}
			var g string
			switch role[id] {
			case rolePeg:
				g = stPeg.Render("●")
			case roleEmpty:
				g = stEmpty.Render("·")
			case roleLanding:
				g = stLanding.Render("●") // destination: blue o
			case roleFrom:
				g = stBlue.Render(dirArrow[m.moves[m.idx-1].dir]) // the peg that moved: blue arrow
			case roleSkipped:
				g = stBlue.Render("·") // skipped/removed peg: blue .
			}
			grid.WriteString(g + " ")
		}
		grid.WriteByte('\n')
	}

	// header / progress
	pegs := state.count()
	title := stTitle.Render("Peg Solitaire")
	step := fmt.Sprintf("Move %d / %d", m.idx, len(m.moves))
	prog := progressBar(m.idx, len(m.moves), 24)

	var moveLine string
	if m.idx > 0 {
		moveLine = stKey.Render(arrow+" ") + desc
	} else {
		moveLine = stDim.Render(desc)
	}

	header := lipgloss.JoinHorizontal(lipgloss.Center, title, stDim.Render("   "+step))
	board := strings.TrimRight(grid.String(), "\n")
	body := lipgloss.JoinVertical(lipgloss.Center,
		header, prog, "", board, "", stDim.Render(fmt.Sprintf("pegs left: %d", pegs)))

	legend := "  " + stBlue.Render("→") + stDim.Render(" moved peg   ") +
		stBlue.Render("●") + stDim.Render(" landed   ") +
		stBlue.Render("·") + stDim.Render(" removed   ") +
		stPeg.Render("●") + stDim.Render(" peg   ") +
		stEmpty.Render("·") + stDim.Render(" empty")

	help := stKey.Render("←/tab") + stDim.Render(" prev   ") +
		stKey.Render("→/space") + stDim.Render(" next   ") +
		stKey.Render("↑/f") + stDim.Render(" first   ") +
		stKey.Render("↓/l") + stDim.Render(" last   ") +
		stKey.Render("q") + stDim.Render(" quit")

	content := lipgloss.JoinVertical(lipgloss.Center,
		body, "", moveLine, legend, "", help)

	// Fill the entire terminal with the content centered (no border).
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
