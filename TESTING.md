# Testing

```bash
make test     # the suite, about half a minute
make bench    # the comparative benchmarks, about ten minutes
```

Everything in `internal/bench` is opt in, because together the benchmarks run past the
default `go test` timeout. `go test ./...` skips them and stays green; `make bench`
sets `PERRY_BENCH=1` and a longer timeout. Nothing in that package asserts a
correctness property that is not also covered in `internal/api`, `internal/pack` or
`internal/server`.

CI runs `gofmt`, `go vet`, `go build`, and the suite twice: once plain and once under
`-race`. The server is concurrent by design, so a data race there is a correctness bug
that ordinary tests pass straight through.

## Checked against git itself

A reimplementation that agrees only with itself proves nothing, so every layer is
cross-checked against the `git` binary.

| Check | Result |
|---|---|
| `git hash-object` vs. our hashing, four payload shapes up to 800 KB | Match |
| `git hash-object -t tree` vs. our tree encoder and sort order | Match |
| Read a pack git wrote: 1078 objects, 745 deltas | Every hash verified |
| `git cat-file -t` vs. our decoded object type | Match |
| `git index-pack` on a pack we wrote | Accepted |
| `git verify-pack` on an index we wrote | Accepted, 1078 offsets match |
| Delta round trip, 300 random insert/delete/overwrite mutations | Pass |
| `git push` to Perry, `git clone` back | Identical tree, `git fsck` clean |
| Incremental `git fetch` after a second push | Only new objects sent |
| Push with a stale ref, from `git` | Refused |
| 20 `git push` clients at once | 20/20 landed |
| Commit built through the API, then `git clone` | Clones and checks out cleanly |
| Commit built over MCP, then `git clone` of that branch | Carries the agent's edit |

## The MCP adapter

Tested as a protocol, not as a set of helpers. A session is driven over real pipes
with newline-delimited JSON-RPC, and the end-to-end test walks the whole agent loop,
locate to read to branch to commit, then reads the result back out of the store rather
than through the path that wrote it.

Separate tests pin down the parts that fail quietly:

- version negotiation
- a tool failure arriving as a tool result rather than as a transport error the model
  cannot see
- a stale `expect` being refused
- notifications drawing no reply, so the response stream never shifts by one

## Corruption

Bad magic, wrong version, truncated streams, flipped bytes in the body and in the
trailer, reserved delta opcodes, copies running past the end of a base, and declared
sizes that disagree with the payload.

## The agent benchmarks

These need a logged-in Claude Code CLI and spend real money, so they are gated twice
over and never run in CI.

```bash
make bench-agent   # one task, three trials per cell, two latencies
make bench-eval    # twenty tasks across two corpora
make bench-code    # three real coding tasks: edit, build, test
```

`TestCodeTaskGrading` is the exception: it checks the coding-task grader with no model
and no API key, under the ordinary `PERRY_BENCH` gate. For each task the repository as
the agent first finds it must be graded a failure, and a repository with a tampered
test file must be rejected. A grader that passed everything would otherwise report
four perfect arms and mean nothing.
