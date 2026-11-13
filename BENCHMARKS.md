# Benchmarks

```sh
make corpora   # clone the three corpora into testdata/, about 50 MB, cached
make bench     # run everything, about 10 minutes
```

Or one at a time:

```sh
make bench-setup        # what it costs an agent to reach one file
make bench-contention   # what happens when agents write to the same branch
```

Without `make`:

```sh
sh scripts/corpora.sh
go test ./internal/bench -v -timeout 40m
```

Every benchmark in `internal/bench` is opt in, because together they run past
the default `go test` timeout. `go test ./...` skips them and stays green;
`make bench` sets `PERRY_BENCH=1` and a longer timeout. Nothing in that package
asserts a correctness property that is not also covered in `internal/api`,
`internal/pack` or `internal/server`, so the default run loses measurements,
not coverage.

The benchmarks also skip themselves, with instructions, when the corpora are
not present.

## What is measured

Every number below is from a real repository cloned at full depth. An earlier
version of this suite used a generated corpus, which made two of the baselines
meaningless: with a single commit, `--depth 1` is the whole repository, and
four hundred files of repeated filler text compress and grep nothing like
source does.

| Corpus | Commits | Files | Packed |
|---|---:|---:|---:|
| `cobra-full.git` | 1,106 | 66 | 2.16 MB |
| `grpc-go.git` | 6,100 | 1,424 | 45.95 MB |

The git baselines are served over `file://`, which is a faster transport than
HTTP. Perry is measured over HTTP. Every comparison here is therefore
conservative: a win against `file://` is a win against a real remote.

All figures are one machine, AMD Ryzen 7 5800H, Windows 11, Go 1.26.

## Setup cost

The task is identical on every path: find the file containing a given symbol
and have its contents in hand. That is the minimum before an agent can edit
anything. The sparse-checkout path is additionally *told* which directory to
look in, which the others have to work out for themselves.

### grpc-go

| Path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone + `git grep` | 4.07s | 45.94 MB | 60.67 MB |
| shallow clone `--depth 1` | 5.00s | 3.39 MB | 18.10 MB |
| shallow + `--filter=blob:none` | 5.43s | 3.45 MB | 18.28 MB |
| shallow + sparse, directory known | 1.99s | 737 KB | 3.08 MB |
| warm worktree, repository on disk | 1.79s | 0 B | 14.53 MB |
| **perry API** | **956ms** | **27.2 KB** | **0 B** |

### cobra

| Path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone + `git grep` | 526ms | 2.16 MB | 2.87 MB |
| shallow clone `--depth 1` | 511ms | 222 KB | 941 KB |
| shallow + `--filter=blob:none` | 650ms | 224 KB | 943 KB |
| shallow + sparse, directory known | 925ms | 127 KB | 201 KB |
| warm worktree, repository on disk | 207ms | 0 B | 689 KB |
| **perry API** | **72ms** | **5.7 KB** | **0 B** |

Reading the table:

- **Against a full clone**, perry moves 1,690x fewer bytes on grpc-go. Both
  sides of that ratio are bytes on the wire.
- **Against the best git path**, shallow plus a sparse checkout, perry is
  about twice as fast and moves 27x fewer bytes, while finding the file on its
  own rather than being told where it is.
- **A blind `blob:none` grep is the slowest path on the board.** Lazy blob
  fetch defeats a search that does not already know its answer. Server-side
  grep is the structural difference, not the transport.
- **Shallow clone is slower than a full clone on grpc-go**, 5.00s against
  4.07s. Walking 6,100 commits to compute the shallow boundary costs more than
  sending the pack does over a local transport. Over a real network the 13x
  byte saving would reverse that.
- **Only perry leaves nothing behind.** Every git path leaves between 201 KB
  and 60 MB on disk. Across a fleet that is the figure that multiplies.

### Ingest

Pushing a repository into perry is paid once: 1.3s for cobra, 22.7s for
grpc-go. A comparison against "clone it from GitHub each time" should count it.

## Same-ref contention

Every agent appends its own line to one file on one branch. Every writer has to land
and no writer may lose another's line, which the test checks by counting the distinct
lines that survived. Perry reads the tip, reads the file and commits with a
compare-and-swap, retrying on a 409; the git agents push and, on rejection, reset to
the new tip and retry. Both run over HTTP. The git clones happen before the clock
starts, so this measures the push war alone.

| Agents | Perry in process | Perry over HTTP | Retries each | Git | Retries each | Ratio |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 4 ms | 4 ms | 1.0 | 494 ms | 1.0 | 135x |
| 5 | 46 ms | 33 ms | 1.8 | 3.84 s | 3.0 | 117x |
| 20 | 115 ms | 146 ms | 1.6 | 28.93 s | 10.5 | 198x |
| 50 | 430 ms | 393 ms | 2.3 | 2 m 26 s | 24.4 | 371x |

No lost updates on either side at any size: all 1, 5, 20 and 50 lines survived. Git is
correct here, just expensive.

**Read the one-agent row first.** Nobody contends at one agent, so it isolates fixed
overhead: git spawns five processes and writes a working tree per attempt, perry makes
three HTTP calls. That alone is 135x, and it is not a claim about concurrency. The
honest concurrency result is the *growth* from 135x to 371x — what contention adds on
top of the overhead gap.

**The retry counts are the design result.** Git's grow linearly with the agent count,
so total work grows quadratically: at fifty agents each one pushes 24.4 times to land
once, 1220 attempts in all. Perry's stay near flat at 2.3, 117 attempts in all, because
a losing writer re-reads one ref row rather than re-running a fetch, a reset and a push.

**Perry over HTTP matches perry in process to within noise**, so JSON encoding and the
round trips are not hiding anything at this scale. Perry over HTTP sometimes records
*fewer* retries than in process, because the slower path spreads the agents out and
they collide less.

## Known limits

- One machine, one operator, two Go repositories.
- The git baselines run over `file://`, not HTTP, which favours git.
- Perry does not serve shallow or partial clones. `--depth` is refused
  outright; `--filter` is silently ignored and the client gets a full clone.
  An agent that needs a real checkout, to run a build or a test suite, pays
  more against perry than against GitHub.
- Nothing here measures a complete coding task. These are access and write
  costs, not end-to-end task completion.
- The contention benchmark's HTTP runs are loopback. A remote agent would also
  pay network latency on each of the three requests per attempt, and a remote
  git agent would pay it per subprocess round trip too.
- Git's clones in the contention benchmark happen before the clock starts.
  Including them would widen the gap.
