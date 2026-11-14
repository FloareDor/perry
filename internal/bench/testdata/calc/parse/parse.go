// Package parse builds a syntax tree from an arithmetic expression.
//
// The grammar is the usual one, with multiplication binding tighter than
// addition and both associating to the left:
//
//	expr    := term (("+" | "-") term)*
//	term    := unary (("*" | "/") unary)*
//	unary   := "-" unary | primary
//	primary := number | "(" expr ")"
package parse

import (
	"fmt"

	"example.com/calc/lex"
)

// Node is one node of the syntax tree.
type Node interface{ node() }

// Num is a literal number.
type Num struct{ Value float64 }

// Unary is a prefix operator applied to one operand.
type Unary struct {
	Op string
	X  Node
}

// Binary is an infix operator applied to two operands.
type Binary struct {
	Op   string
	L, R Node
}

func (Num) node()    {}
func (Unary) node()  {}
func (Binary) node() {}

type parser struct {
	toks []lex.Token
	i    int
}

// Parse tokenises and parses src.
func Parse(src string) (Node, error) {
	toks, err := lex.Scan(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.Kind != lex.EOF {
		return nil, fmt.Errorf("unexpected %s", t.Kind)
	}
	return n, nil
}

func (p *parser) peek() lex.Token { return p.toks[p.i] }

func (p *parser) next() lex.Token {
	t := p.toks[p.i]
	if t.Kind != lex.EOF {
		p.i++
	}
	return t
}

func (p *parser) expr() (Node, error) {
	n, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().Kind
		if k != lex.Plus && k != lex.Minus {
			return n, nil
		}
		op := p.next().Kind.String()
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		n = Binary{Op: op, L: n, R: r}
	}
}

func (p *parser) term() (Node, error) {
	n, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().Kind
		if k != lex.Star && k != lex.Slash {
			return n, nil
		}
		op := p.next().Kind.String()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		n = Binary{Op: op, L: n, R: r}
	}
}

func (p *parser) unary() (Node, error) {
	if p.peek().Kind == lex.Minus {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return Unary{Op: "-", X: x}, nil
	}
	return p.primary()
}

func (p *parser) primary() (Node, error) {
	t := p.next()
	switch t.Kind {
	case lex.Num:
		return Num{Value: t.Num}, nil
	case lex.LParen:
		n, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.peek().Kind != lex.RParen {
			return nil, fmt.Errorf("missing )")
		}
		p.next()
		return n, nil
	}
	return nil, fmt.Errorf("unexpected %s", t.Kind)
}
