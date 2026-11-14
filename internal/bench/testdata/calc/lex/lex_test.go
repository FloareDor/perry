package lex_test

import (
	"testing"

	"example.com/calc/lex"
)

func TestScanKinds(t *testing.T) {
	toks, err := lex.Scan("1 + 2*3")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []lex.Kind{lex.Num, lex.Plus, lex.Num, lex.Star, lex.Num, lex.EOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d is %v, want %v", i, toks[i].Kind, k)
		}
	}
}

func TestScanPositions(t *testing.T) {
	toks, err := lex.Scan("12 + 345")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []int{0, 3, 5, 8}
	for i, p := range want {
		if toks[i].Pos != p {
			t.Errorf("token %d starts at %d, want %d", i, toks[i].Pos, p)
		}
	}
}

func TestScanNumbers(t *testing.T) {
	toks, err := lex.Scan("1.5")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if toks[0].Num != 1.5 {
		t.Errorf("got %v, want 1.5", toks[0].Num)
	}
}

func TestScanRejectsJunk(t *testing.T) {
	if _, err := lex.Scan("1 @ 2"); err == nil {
		t.Fatal("Scan accepted an unexpected character")
	}
}
