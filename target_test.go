package main

import (
	"testing"
	"time"
)

func resetTarget() {
	pathMoves = nil
	noMoveUp = false
	targetMoves = 0
	timeout = 0
	timeoutSet = false
	moveUpTimeout = defaultMoveUpTimeout
}

// Exact counts are honored and solutions valid; also reports search speed.
func TestTargetValidExact(t *testing.T) {
	initBoard(7, 2)
	maxPegsDB = 10
	for _, tg := range []int{31, 30, 28, 26, 25, 24} {
		resetTarget()
		targetMoves = tg
		noMoveUp = true // no timeout: run to completion
		t0 := time.Now()
		if !solve() {
			t.Fatalf("target %d: no solution found", tg)
		}
		validateSolution(t)
		if len(pathMoves) != tg {
			t.Fatalf("target %d: got %d moves", tg, len(pathMoves))
		}
		t.Logf("exact %-2d moves in %-8v nodes=%d", tg, time.Since(t0).Round(time.Millisecond), tgtNodes)
	}
}

// Move-up with the default per-target timeout: a hard low target climbs and
// returns *some* valid solution quickly.
func TestMoveUpClimbsQuickly(t *testing.T) {
	initBoard(7, 2)
	maxPegsDB = 10
	resetTarget()
	targetMoves = 18 // optimum-ish: hard to hit exactly, so it should climb
	moveUpTimeout = 300 * time.Millisecond
	t0 := time.Now()
	if !solve() {
		t.Fatal("move-up should always yield a solution near the top")
	}
	validateSolution(t)
	el := time.Since(t0)
	t.Logf("move-up from 18 -> %d moves in %v", len(pathMoves), el.Round(time.Millisecond))
	if len(pathMoves) < 18 {
		t.Fatalf("solution shorter than target?! %d", len(pathMoves))
	}
}

// --no-move-up with --timeout gives up within roughly the limit.
func TestNoMoveUpTimeout(t *testing.T) {
	initBoard(7, 2)
	maxPegsDB = 10
	resetTarget()
	targetMoves = 18
	noMoveUp = true
	timeout, timeoutSet = 500*time.Millisecond, true
	t0 := time.Now()
	ok := solve()
	el := time.Since(t0)
	t.Logf("no-move-up target 18 timeout 500ms: ok=%v elapsed=%v", ok, el.Round(time.Millisecond))
	if ok {
		t.Skip("found 18 within 500ms (fine); timeout path not exercised")
	}
	if el > 3*time.Second {
		t.Fatalf("timeout not honored: ran %v", el)
	}
}
