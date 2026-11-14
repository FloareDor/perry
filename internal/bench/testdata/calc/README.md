# calc

A small arithmetic expression evaluator, in three packages:

- `lex` turns a string into tokens.
- `parse` turns tokens into a syntax tree, honouring operator precedence.
- `eval` walks the tree and produces a number.

`cmd/calc` is a one-argument command line front end:

    $ go run ./cmd/calc '2 + 3 * (4 - 1)'
    11

Run the tests with `go test ./...`. There are no dependencies outside the
standard library.
