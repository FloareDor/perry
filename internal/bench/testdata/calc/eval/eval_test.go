package eval_test

import (
	"math"
	"testing"

	"example.com/calc/eval"
)

func TestEval(t *testing.T) {
	cases := []struct {
		src  string
		want float64
	}{
		{"1", 1},
		{"1 + 2", 3},
		{"1 + 2 * 3", 7},
		{"(1 + 2) * 3", 9},
		{"10 - 4 - 3", 3},
		{"8 / 4 / 2", 1},
		{"100 / 5 / 2", 10},
		{"2 * 3 - 4 / 2", 4},
		{"-5 + 2", -3},
		{"-(2 + 3)", -5},
		{"--4", 4},
		{"1.5 * 4", 6},
	}
	for _, c := range cases {
		got, err := eval.Eval(c.src)
		if err != nil {
			t.Errorf("Eval(%q): %v", c.src, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Eval(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	for _, src := range []string{"1 +", "@", "(1"} {
		if _, err := eval.Eval(src); err == nil {
			t.Errorf("Eval(%q) returned no error", src)
		}
	}
}
