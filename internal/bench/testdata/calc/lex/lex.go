// Package lex turns an arithmetic expression into a flat list of tokens.
package lex

import (
	"fmt"
	"strconv"
)

// Kind is the sort of a token.
type Kind int

// The token kinds. EOF is the zero value so that an uninitialised Token is
// harmless to peek at.
const (
	EOF Kind = iota
	Num
	Plus
	Minus
	Star
	Slash
	LParen
	RParen
)

func (k Kind) String() string {
	switch k {
	case EOF:
		return "end of input"
	case Num:
		return "number"
	case Plus:
		return "+"
	case Minus:
		return "-"
	case Star:
		return "*"
	case Slash:
		return "/"
	case LParen:
		return "("
	case RParen:
		return ")"
	}
	return "unknown"
}

// Token is one lexed element. Pos is the byte offset it started at, which is
// what an error message needs in order to point at the right place.
type Token struct {
	Kind Kind
	Num  float64
	Pos  int
}

// Scan tokenises src. The returned slice always ends with an EOF token, so a
// parser can look ahead without bounds checking.
func Scan(src string) ([]Token, error) {
	var out []Token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case isDigit(c) || c == '.':
			j := i
			for j < len(src) && (isDigit(src[j]) || src[j] == '.') {
				j++
			}
			v, err := strconv.ParseFloat(src[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("bad number %q", src[i:j])
			}
			out = append(out, Token{Kind: Num, Num: v, Pos: i})
			i = j
		default:
			k, ok := punct(c)
			if !ok {
				return nil, fmt.Errorf("unexpected character %q", string(c))
			}
			out = append(out, Token{Kind: k, Pos: i})
			i++
		}
	}
	out = append(out, Token{Kind: EOF, Pos: len(src)})
	return out, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func punct(c byte) (Kind, bool) {
	switch c {
	case '+':
		return Plus, true
	case '-':
		return Minus, true
	case '*':
		return Star, true
	case '/':
		return Slash, true
	case '(':
		return LParen, true
	case ')':
		return RParen, true
	}
	return EOF, false
}
