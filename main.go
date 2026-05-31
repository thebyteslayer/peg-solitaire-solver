package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func rcStr(id int) string { return fmt.Sprintf("(%d,%d)", id2r[id], id2c[id]) }

func describeMove(m Move) string {
	overs := make([]string, len(m.overs))
	for i, o := range m.overs {
		overs[i] = rcStr(o)
	}
	s := fmt.Sprintf("peg %s jumps %-5s over %-13s landing %s",
		rcStr(m.from), dirName[m.dir], strings.Join(overs, ","), rcStr(m.to))
	if len(m.overs) > 1 {
		s += fmt.Sprintf("   [multi-jump x%d]", len(m.overs))
	}
	return s
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func main() {
	// Two positional arguments: <size> <cut>. Defaults: 7 2 (English board).
	n, cut := 7, 2
	if len(os.Args) > 1 {
		n = atoiOr(os.Args[1], n)
	}
	if len(os.Args) > 2 {
		cut = atoiOr(os.Args[2], cut)
	}

	if n%2 == 0 || n < 3 || cut < 1 || 2*cut >= n {
		fmt.Printf("usage: solver <size> <cut>   (size must be odd, 0 < cut < size/2)\n")
		return
	}

	initBoard(n, cut)
	start := startState()

	// Pick an endgame-DB depth that suits the board (bigger boards need a
	// deeper DB; small ones stay tiny). Keeps memory modest.
	maxPegsDB = 10
	if nCells > 40 {
		maxPegsDB = 12
	}

	// Parity check: is the single-center-peg goal even reachable on this board?
	if classOf(start) != classOf(goalState()) {
		fmt.Printf("This %dx%d board (minus %dx%d corners, %d cells) is provably UNSOLVABLE\n",
			n, n, cut, cut, nCells)
		fmt.Println("for the central game: the color invariant forbids ending on a single center peg.")
		return
	}

	p := tea.NewProgram(newTUIModel(start), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("TUI error:", err)
	}
}
