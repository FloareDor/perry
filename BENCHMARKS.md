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

The setup and whole-task benchmarks serve every path over real smart HTTP --
git's through `git http-backend`, perry's through its own server -- and count
bytes in both directions at one shared proxy. The contention benchmark still
uses `file://` for its git side, which is a faster transport than HTTP and so
favours git there.

All figures are one machine, AMD Ryzen 7 5800H, Windows 11, Go 1.26.

## Setup cost

The task is identical on every path: find the file containing a given symbol
and have its contents in hand. That is the minimum before an agent can edit
anything. The sparse-checkout path is additionally *told* which directory to
look in, which the others have to work out for themselves.

### grpc-go

| Path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone + `git grep` | 5.55s | 44.44 MB | 60.67 MB |
| shallow clone `--depth 1` | 4.77s | 3.37 MB | 18.11 MB |
| shallow + `--filter=blob:none` | 5.48s | 3.46 MB | 18.28 MB |
| shallow + sparse, directory known | 2.23s | 746 KB | 3.08 MB |
| warm worktree, repository on disk | 1.53s | 0 B | 14.53 MB |
| **perry API** | **728ms** | **27.2 KB** | **0 B** |

### cobra

| Path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone + `git grep` | 702ms | 2.04 MB | 2.87 MB |
| shallow clone `--depth 1` | 644ms | 222 KB | 941 KB |
| shallow + `--filter=blob:none` | 854ms | 225 KB | 943 KB |
| shallow + sparse, directory known | 1.17s | 128 KB | 201 KB |
| warm worktree, repository on disk | 195ms | 0 B | 689 KB |
| **perry API** | **46ms** | **5.7 KB** | **0 B** |

Reading the table:

- **Against a full clone**, perry moves about 1,600x fewer bytes on grpc-go.
- **Against the best git path that transfers anything**, shallow plus a sparse
  checkout, perry is roughly 3x faster and moves about 27x fewer bytes, while
  finding the file on its own rather than being told where it is.
- **The warm worktree transfers nothing at all**, because its objects are
  already local. Perry does not beat it on bytes and never will. What it does
  is reach the same answer without the clone that put those objects there, and
  without leaving 14.53 MB on disk.
- **A blind `blob:none` grep is the slowest path on the board.** Lazy blob
  fetch defeats a search that does not already know its answer. Server-side
  grep is the structural difference, not the transport.
- **Only perry leaves nothing behind.** Every git path leaves between 201 KB
  and 60 MB on disk. Across a fleet that is the figure that multiplies.

### How the bytes are counted

Every path, git and perry alike, runs over real HTTP through one counting
proxy, so "wire" means the same thing in every row.

It did not always. An earlier version of this file read git's transfer from
`git count-objects -v`, which reports what git materialised on disk, and
perry's from the length of its HTTP response bodies. A pack on disk is not the
bytes that arrived, and comparing one against the other is not a wire-to-wire
comparison however close the two usually are. The git baselines also ran over
`file://`, which transfers nothing over any socket and so cannot be counted at
all; they now run over `git http-backend`, which costs git the advantage
`file://` was giving it in this table.

Two numbers moved as a result. Perry's reported time against grpc-go fell from
956ms to 728ms, because the old measurement ran the search twice -- once over
HTTP and once in process to find out which path had matched -- and charged
perry for both while counting the bytes of only one. The git rows got slower,
because HTTP is slower than `file://`.

### Ingest

Pushing a repository into perry is paid once: 1.1s for cobra, 22.2s for
grpc-go. A comparison against "clone it from GitHub each time" should count it.

## The whole task, with latency

A faster git operation is not a faster agent. This measures the whole unit of
work -- locate the symbol, read its file, edit it, publish the edit -- from
nothing on disk to the change being durable on the server. Setup is on the
clock for every path that needs it.

Agents do not run next to their git host, so the same proxy adds a fixed delay
to every request on both sides. It models round trips only: no bandwidth
limit, no slow start, no congestion, all of which would punish the byte-heavy
paths further.

### grpc-go, time to finish the task

| Path | 0 ms | 25 ms | 75 ms | Wire | Requests |
|---|---:|---:|---:|---:|---:|
| full clone | 5.99s | 6.34s | 6.38s | 44.46 MB | 5 |
| shallow `--depth 1` | 5.58s | 5.85s | 5.98s | 3.39 MB | 5 |
| shallow + sparse, directory known | 2.97s | 3.02s | 3.59s | 769 KB | 9 |
| warm worktree + fetch | 3.03s | 3.05s | 2.99s | 45.9 KB | 4 |
| **perry API** | **427ms** | **617ms** | **718ms** | **63.3 KB** | **4** |

### cobra, time to finish the task

| Path | 0 ms | 25 ms | 75 ms | Wire | Requests |
|---|---:|---:|---:|---:|---:|
| full clone | 1.29s | 1.35s | 1.69s | 2.05 MB | 5 |
| shallow `--depth 1` | 1.11s | 1.26s | 1.56s | 225 KB | 5 |
| shallow + sparse, directory known | 1.70s | 1.92s | 2.39s | 131 KB | 9 |
| warm worktree + fetch | 975ms | 1.09s | 1.32s | 5.9 KB | 4 |
| **perry API** | **47ms** | **143ms** | **351ms** | **12.3 KB** | **4** |

Reading the table:

- **Latency does not change the ordering**, and it does not flatter perry
  either. Every path pays round trips times latency. Perry makes four requests
  against git's four to nine, so the *ratio* narrows as the link gets worse --
  7x down to 5x against the sparse checkout on grpc-go -- even as the absolute
  gap grows from 2.54s to 2.87s. The advantage is work avoided, not round
  trips saved.
- **Every path is verified to have finished.** The edit has to be readable
  back from the server with a unique marker in it, or the row fails the test
  rather than reporting a time. This caught a real bug: `worktree add
  --detach` leaves HEAD detached, so the warm path's `git push HEAD:main`
  could not resolve its destination.
- **The warm worktree still moves fewer bytes than perry**, 45.9 KB against
  63.3 KB, for the same reason as above. It pays for that in 14.53 MB of disk
  and in having to keep a clone current.

### What this does not show

A real agent finishing a real task sooner. The edit is scripted, it is one
agent, and no model is in the loop. That measurement is the next section, and
it does not go perry's way.

## A real agent, with a model in the loop

Every benchmark above measures perry against git. This one measures an agent
against an agent: the same task, the same model, once with only perry's MCP
tools and once with a shell and an optimised checkout. It is the measurement
the rest of this document was carefully not making.

`claude-sonnet-5-5`, pinned on both sides. grpc-go, 6100 commits, 1424 files.
Three trials per cell, every one of the 18 runs completed the task, and
completion means the marker is readable back off the server -- from the perry
store for the MCP arm, from `git grep` on the origin for the git arms. The
perry arm gets no shell, so it cannot fall back on a clone; the git arms get
no perry tools.

### Median of three, grpc-go

| Arm | RTT | Setup | Agent | **Total** | Turns | Output tokens | Cost | Wire |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| perry (MCP) | 0 ms | 0 ms | 74.9 s | **77.6 s** | 6 | 15,040 | $0.33 | 63.5 KB |
| git shallow | 0 ms | 4.7 s | 16.5 s | **23.6 s** | 4 | 585 | $0.07 | 3.39 MB |
| git sparse, told the dir | 0 ms | 2.3 s | 15.9 s | **20.6 s** | 4 | 727 | $0.08 | 769 KB |
| perry (MCP) | 75 ms | 0 ms | 75.8 s | **78.2 s** | 6 | 15,070 | $0.33 | 63.8 KB |
| git shallow | 75 ms | 5.0 s | 19.4 s | **27.0 s** | 4 | 565 | $0.07 | 3.39 MB |
| git sparse, told the dir | 75 ms | 2.8 s | 14.5 s | **19.6 s** | 4 | 623 | $0.07 | 769 KB |

**Perry loses, by a factor of three to four.** It moves 53 times fewer bytes
and takes 3.3 to 4.0 times longer, at 4.6 times the cost. This is not noise:
the three perry trials at 0 ms land within 1.8 s of each other, 74.0 to
75.8 s, and their output token counts within 126 of each other.

### Why

The agent had to rewrite `balancer/rls/balancer.go`, 27,314 bytes and 733
lines, and `perry_commit` takes each file's **complete new contents**. So the
model re-emitted the whole file: 15,040 output tokens against the git arm's
585, which used `Edit` and sent a few lines.

The arithmetic holds up. Both arms carry about 15 s of fixed turn overhead.
Perry's extra 59 s buys 14,455 extra output tokens, a marginal generation rate
of roughly 245 tokens per second, which is what this model does. The latency
is not transport, parsing or SQLite. It is the model typing out a file it had
just been shown.

That design decision was deliberate and it is argued for in the commit that
introduced the adapter: a model emitting a unified diff against a file it read
is the most common way an agent's edit silently fails to apply. That reasoning
is about reliability and it still stands. It was asserted about cost without
being measured, and measured, it is wrong. A whole-file write trades a byte
saving nobody was bottlenecked on for a token cost that dominates the clock.

This is perry's problem and not the adapter's. `POST /v1/repos/{repo}/commit`
takes whole-file contents too, so any client built on it pays the same price.

### What perry does win, and what it is worth

Latency-insensitivity is real and it is measurable. Going from 0 ms to 75 ms
of RTT costs perry 0.6 s and costs the shallow clone 3.4 s, because perry makes
four requests against git's five to nine. Setup is genuinely free: 0 ms against
2.3 to 5.0 s, every run.

Both are true and neither is worth 57 s. An agent's wall clock is dominated by
inference, and a transport that is 53 times leaner is invisible next to a tool
design that makes the model generate twenty-five times more text.

### What this still does not show

- One task, one repository, one model, one file size. The whole gap is a
  function of how large the edited file is; on a 50-line file the token cost
  of rewriting it would be close to nothing and the ordering could reverse.
  The task was not chosen to favour either side, but it was not varied either.
- Token counts are not cleanly comparable between arms. Each carries a
  different fixed system prompt, so only the difference between arms means
  anything, not the absolute figures.
- The git arms use tools this model has seen endlessly in training; the perry
  arm gets seven it has never seen. That cuts against perry and cannot be
  corrected for here.
- One run reached for PowerShell, which was not in its allowed list, and
  adapted. Tool-set fit is a confound in both directions.

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
- The setup and whole-task benchmarks run every path over real HTTP and count
  bytes at one shared proxy. The contention benchmark does not: its git side
  is still measured as git materialised it, and its runs are loopback.
- Injected latency models round trips only. There is no bandwidth limit, no
  slow start and no congestion, each of which would cost the byte-heavy paths
  more than it costs perry.
- Perry does not serve shallow or partial clones. `--depth` is refused
  outright; `--filter` is silently ignored and the client gets a full clone.
  An agent that needs a real checkout, to run a build or a test suite, pays
  more against perry than against GitHub.
- The real-agent comparison is one task, one repository, one model and one
  file size, at three trials per cell. Its result -- that perry loses by three
  to four times on a 733-line file -- is a function of the edited file's size,
  and it was not varied.
- The contention benchmark's HTTP runs are loopback. A remote agent would also
  pay network latency on each of the three requests per attempt, and a remote
  git agent would pay it per subprocess round trip too.
- Git's clones in the contention benchmark happen before the clock starts.
  Including them would widen the gap.
