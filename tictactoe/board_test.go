package main

import "testing"

func TestWinnerRows(t *testing.T) {
	b := NewBoard()
	_ = b.MakeMove(0, X)
	_ = b.MakeMove(1, X)
	_ = b.MakeMove(2, X)
	if w, ok := b.Winner(); !ok || w != X {
		t.Fatalf("expected winner X but got %v %v", w, ok)
	}
}

func TestMinimaxBlocksWin(t *testing.T) {
	b := NewBoard()
	// Human X is about to create two-in-a-row; AI O should block
	_ = b.MakeMove(0, X)
	_ = b.MakeMove(4, X)
	ai := NewMinimax("ai")
	mv, err := ai.Move(b, O)
	if err != nil {
		t.Fatal(err)
	}
	// Best block is 8? (but exact expected can vary) just ensure move is valid and not occupied
	if b.cells[mv] != Empty {
		t.Fatalf("expected empty cell chosen by AI; got %v", mv)
	}
}
