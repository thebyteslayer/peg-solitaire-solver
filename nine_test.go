package main

import "testing"

// Regression for the reported issue: `9 3 --target-moves 42 --no-move-up` used
// to never finish. It must now produce a valid 42-move solution quickly.
func TestNineTarget42(t *testing.T) {
	initBoard(9, 3)
	maxPegsDB = 12
	pathMoves = nil
	targetMoves, noMoveUp = 42, true
	timeout, timeoutSet, moveUpTimeout = 0, false, defaultMoveUpTimeout
	if !solve() {
		t.Fatal("no solution")
	}
	validateSolution(t)
	if len(pathMoves) != 42 {
		t.Fatalf("want 42 moves, got %d", len(pathMoves))
	}
}
