// Package eval evaluates the syntax tree that parse produces.
package eval

import (
	"fmt"

	"example.com/calc/parse"
)

// Eval parses and evaluates an arithmetic expression.
func Eval(src string) (float64, error) {
	n, err := parse.Parse(src)
	if err != nil {
		return 0, err
	}
	return node(n)
}

func node(n parse.Node) (float64, error) {
	switch n := n.(type) {
	case parse.Num:
		return n.Value, nil

	case parse.Unary:
		x, err := node(n.X)
		if err != nil {
			return 0, err
		}
		if n.Op == "-" {
			return -x, nil
		}
		return 0, fmt.Errorf("unknown unary operator %q", n.Op)

	case parse.Binary:
		l, err := node(n.L)
		if err != nil {
			return 0, err
		}
		r, err := node(n.R)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case "+":
			return l + r, nil
		case "-":
			return l - r, nil
		case "*":
			return l * r, nil
		case "/":
			return l / r, nil
		}
		return 0, fmt.Errorf("unknown operator %q", n.Op)
	}
	return 0, fmt.Errorf("unknown node %T", n)
}
