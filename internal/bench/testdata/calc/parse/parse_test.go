package parse_test

import (
	"testing"

	"example.com/calc/parse"
)

// TestPrecedence pins the shape of the tree, not just the value it evaluates
// to: a parser that got precedence wrong could still produce the right answer
// for a symmetric expression.
func TestPrecedence(t *testing.T) {
	n, err := parse.Parse("1 + 2 * 3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	top, ok := n.(parse.Binary)
	if !ok || top.Op != "+" {
		t.Fatalf("top of the tree is %#v, want a + node", n)
	}
	if _, ok := top.L.(parse.Num); !ok {
		t.Errorf("left of + is %#v, want a number", top.L)
	}
	right, ok := top.R.(parse.Binary)
	if !ok || right.Op != "*" {
		t.Fatalf("right of + is %#v, want a * node", top.R)
	}
}

func TestLeftAssociative(t *testing.T) {
	n, err := parse.Parse("1 - 2 - 3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	top, ok := n.(parse.Binary)
	if !ok || top.Op != "-" {
		t.Fatalf("top of the tree is %#v, want a - node", n)
	}
	// Left associative means the nesting is on the left.
	if _, ok := top.L.(parse.Binary); !ok {
		t.Errorf("left of the outer - is %#v, want another - node", top.L)
	}
	if _, ok := top.R.(parse.Num); !ok {
		t.Errorf("right of the outer - is %#v, want a number", top.R)
	}
}

func TestParens(t *testing.T) {
	n, err := parse.Parse("(1 + 2) * 3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	top, ok := n.(parse.Binary)
	if !ok || top.Op != "*" {
		t.Fatalf("top of the tree is %#v, want a * node", n)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"1 +", "(1 + 2", "1 + * 2", ""} {
		if _, err := parse.Parse(src); err == nil {
			t.Errorf("Parse(%q) returned no error", src)
		}
	}
}
