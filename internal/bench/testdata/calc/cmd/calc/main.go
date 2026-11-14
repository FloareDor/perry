// Command calc evaluates one arithmetic expression given as its argument.
package main

import (
	"fmt"
	"os"
	"strconv"

	"example.com/calc/eval"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: calc <expression>")
		os.Exit(2)
	}
	v, err := eval.Eval(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "calc:", err)
		os.Exit(1)
	}
	fmt.Println(strconv.FormatFloat(v, 'g', -1, 64))
}
