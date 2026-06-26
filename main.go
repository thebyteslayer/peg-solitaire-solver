package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// parseDuration accepts Go durations like "500ms" or "5s", and also a bare
// number which is interpreted as seconds.
func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(v * float64(time.Second)), nil
}

func rcStr(id int) string { return fmt.Sprintf("(%d,%d)", id2r[id], id2c[id]) }

func describeMove(m Move) string {
	mOvers := oversOf(m)
	overs := make([]string, len(mOvers))
	for i, o := range mOvers {
		overs[i] = rcStr(o)
	}
	s := fmt.Sprintf("peg %s jumps %-5s over %-13s landing %s",
		rcStr(m.from), dirName[m.dir], strings.Join(overs, ","), rcStr(m.to))
	turning := false
	for _, d := range m.turns {
		if int(d) != m.dir {
			turning = true
			break
		}
	}
	switch {
	case turning:
		s += fmt.Sprintf("   [corner sweep x%d]", len(mOvers))
	case len(mOvers) > 1:
		s += fmt.Sprintf("   [multi-jump x%d]", len(mOvers))
	}
	return s
}

func usageText() string {
	return "usage: solver [--type T] [--target-moves N] [--no-move-up] [--timeout D] [--move-up-timeout D] <size> <cut>\n" +
		"  --type   board geometry (default symmetrical): symmetrical | european | asymmetrical | diamond\n" +
		"  positional size parameters (override the type's defaults):\n" +
		"    symmetrical   <size> <cut>  size x size minus cut x cut square corners (default 7 2, the English board)\n" +
		"    european      <size> <cut>  like symmetrical, but the corner cutouts are filled with a staircase\n" +
		"                                (default 7 2, 37 holes); target is one cell above center\n" +
		"    asymmetrical  <size> <cut>  symmetrical, but the top row and right column dropped (default 7 2)\n" +
		"    diamond       <radius>      staircase diamond, radius cells from center each way incl. center\n" +
		"                                (default 5, the 41-hole diamond; cut unused)\n" +
		"  durations like 500ms, 5s\n"
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func main() {
	// Two positional arguments: <size> <cut>. Defaults: 7 2 (English board).
	// Flags:
	//   --target-moves N      search for a solution using exactly N moves,
	//                         moving up to N+1, N+2, ... if N isn't reachable
	//                         within the per-target move-up timeout.
	//   --no-move-up          attempt only the exact target; no time limit by
	//                         default (--timeout caps it).
	//   --timeout D           time limit for the --no-move-up search (e.g. 5s).
	//   --move-up-timeout D   per-target budget before climbing (default 1s).
	typeStr := "symmetrical"
	moveUpTimeout = defaultMoveUpTimeout
	var pos []string
	args := os.Args[1:]
	flagVal := func(i *int, a, name string) (string, bool) {
		if a == "--"+name || a == "-"+name {
			if *i+1 < len(args) {
				*i++
				return args[*i], true
			}
			return "", true
		}
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v, true
		}
		if v, ok := strings.CutPrefix(a, "-"+name+"="); ok {
			return v, true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-move-up" || a == "-no-move-up":
			noMoveUp = true
		default:
			if v, ok := flagVal(&i, a, "target-moves"); ok {
				targetMoves = atoiOr(v, 0)
			} else if v, ok := flagVal(&i, a, "timeout"); ok {
				if d, err := parseDuration(v); err == nil {
					timeout, timeoutSet = d, true
				}
			} else if v, ok := flagVal(&i, a, "move-up-timeout"); ok {
				if d, err := parseDuration(v); err == nil {
					moveUpTimeout = d
				}
			} else if v, ok := flagVal(&i, a, "type"); ok {
				typeStr = v
			} else {
				pos = append(pos, a)
			}
		}
	}

	bt, ok := parseBoardType(typeStr)
	if !ok {
		fmt.Printf("unknown --type %q (choose one of: %s)\n",
			typeStr, strings.Join(boardTypeNames, ", "))
		return
	}
	if bt == TypeTriangular {
		fmt.Println("the triangular board is not implemented yet")
		return
	}
	// Each type has its own size defaults; positional <size> <cut> override them.
	sp := defaultSpec(bt)
	if len(pos) > 0 {
		sp.size = atoiOr(pos[0], sp.size)
	}
	if len(pos) > 1 {
		sp.cut = atoiOr(pos[1], sp.cut)
	}
	if msg := sp.validate(); msg != "" {
		fmt.Print(usageText())
		fmt.Printf("\n%s: %s\n", bt, msg)
		return
	}

	initBoardSpec(sp)
	start := startState()

	// Pick an endgame-DB depth that suits the board (bigger boards need a
	// deeper DB; small ones stay tiny). Keeps memory modest.
	maxPegsDB = 10
	if nCells > 40 {
		maxPegsDB = 12
	}

	// Parity check: is the single-center-peg goal even reachable on this board?
	// (Only the square lattice has the mod-3 color invariant; triangular skips it.)
	if hasColorInv && classOf(start) != classOf(goalState()) {
		fmt.Printf("This %s board (size %d, cut %d, %d cells) is provably UNSOLVABLE\n",
			bt, sp.size, sp.cut, nCells)
		fmt.Println("for the central game: the color invariant forbids ending on a single center peg.")
		return
	}

	// In target-move mode the search can legitimately fail (no solution of the
	// requested length, or a --timeout was hit). Run it up front and, if it
	// fails, report it plainly rather than opening an empty TUI.
	if targetMoves > 0 {
		runTargetSearch(start)
		return
	}

	p := tea.NewProgram(newTUIModel(start), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("TUI error:", err)
	}
}

// runTargetSearch performs the exact-move-count search synchronously while
// streaming progress to stderr. On success it opens the TUI to replay the
// solution; on failure it prints why and returns without opening the TUI.
func runTargetSearch(start State) {
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(120 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				if s := getStatus(); s != "" {
					fmt.Fprintf(os.Stderr, "\r\033[K%s", s)
				}
			}
		}
	}()

	t0 := time.Now()
	found := solve()
	close(done)
	fmt.Fprint(os.Stderr, "\r\033[K") // wipe the progress line

	if !found {
		switch {
		case tgtTimedOut:
			fmt.Printf("No %d-move solution found within the time limit.\n", targetMoves)
		case noMoveUp:
			fmt.Printf("No solution with exactly %d moves exists.\n", targetMoves)
		default:
			fmt.Printf("No solution found (target %d exceeds the maximum of %d moves).\n",
				targetMoves, nCells-2)
		}
		return
	}

	moves := append([]Move(nil), pathMoves...)
	p := tea.NewProgram(newSolvedTUIModel(start, moves, time.Since(t0)), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("TUI error:", err)
	}
}
