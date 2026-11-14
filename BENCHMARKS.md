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

It has been run twice. The first run is reported below the second, because
perry lost it and the loss is why the second run reads the way it does.

### Median of three, grpc-go

| Arm | RTT | Setup | Agent | **Total** | Turns | Output tokens | Cost | Wire | Reqs |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| perry (MCP) | 0 ms | 0 ms | 14.2 s | **16.8 s** | 6 | 1,078 | $0.134 | 28.1 KB | 4 |
| git shallow | 0 ms | 4.7 s | 16.6 s | **23.8 s** | 4 | 551 | $0.071 | 3.39 MB | 5 |
| git sparse, told the dir | 0 ms | 2.3 s | 14.9 s | **20.0 s** | 4 | 628 | $0.073 | 769 KB | 9 |
| perry (MCP) | 75 ms | 0 ms | 15.7 s | **18.0 s** | 6 | 1,191 | $0.135 | 28.4 KB | 4 |
| git shallow | 75 ms | 4.9 s | 15.7 s | **23.1 s** | 4 | 586 | $0.071 | 3.39 MB | 5 |
| git sparse, told the dir | 75 ms | 2.8 s | 15.0 s | **20.2 s** | 4 | 614 | $0.072 | 769 KB | 9 |

On this task, perry finishes 1.2 to 1.4 times sooner, moving 27 to 121 times
fewer bytes, and costs 1.9 times as much. One task at three trials per cell
establishes that the ordering can go perry's way, not that it generally does.

Every run, because an LLM's wall clock is not repeatable:

| Arm | RTT | # | Setup | Agent | Total | Turns | Out tok | Cost | Wire |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| perry | 0 ms | 1 | 0 ms | 14.04 s | 16.70 s | 6 | 1,100 | $0.1396 | 28.0 KB |
| perry | 0 ms | 2 | 0 ms | 14.21 s | 16.83 s | 6 | 1,078 | $0.1327 | 28.1 KB |
| perry | 0 ms | 3 | 0 ms | 14.81 s | 17.18 s | 6 | 1,125 | $0.1335 | 28.2 KB |
| shallow | 0 ms | 1 | 4.70 s | 17.43 s | 24.79 s | 4 | 598 | $0.0712 | 3.39 MB |
| shallow | 0 ms | 2 | 4.66 s | 16.63 s | 23.78 s | 4 | 551 | $0.0706 | 3.39 MB |
| shallow | 0 ms | 3 | 4.53 s | 16.19 s | 23.15 s | 4 | 609 | $0.0710 | 3.39 MB |
| sparse | 0 ms | 1 | 2.65 s | 14.95 s | 20.02 s | 4 | 733 | $0.0728 | 769 KB |
| sparse | 0 ms | 2 | 2.28 s | 14.31 s | 19.11 s | 4 | 628 | $0.0715 | 769 KB |
| sparse | 0 ms | 3 | 2.21 s | 16.45 s | 21.02 s | 5 | 765 | $0.0775 | 769 KB |
| perry | 75 ms | 1 | 0 ms | 15.73 s | 18.01 s | 6 | 1,191 | $0.1344 | 28.3 KB |
| perry | 75 ms | 2 | 0 ms | 15.22 s | 17.70 s | 6 | 1,204 | $0.1349 | 28.4 KB |
| perry | 75 ms | 3 | 0 ms | 15.78 s | 18.24 s | 6 | 1,217 | $0.1352 | 28.5 KB |
| shallow | 75 ms | 1 | 4.91 s | 15.68 s | 23.06 s | 4 | 586 | $0.0708 | 3.39 MB |
| shallow | 75 ms | 2 | 4.87 s | 15.03 s | 22.25 s | 4 | 577 | $0.0707 | 3.39 MB |
| shallow | 75 ms | 3 | 4.91 s | 16.79 s | 23.96 s | 4 | 540 | $0.0702 | 3.39 MB |
| sparse | 75 ms | 1 | 2.89 s | 14.73 s | 20.05 s | 4 | 614 | $0.0714 | 769 KB |
| sparse | 75 ms | 2 | 2.83 s | 16.81 s | 22.01 s | 5 | 733 | $0.0772 | 769 KB |
| sparse | 75 ms | 3 | 2.80 s | 15.00 s | 20.18 s | 4 | 655 | $0.0717 | 769 KB |

### The first run, which perry lost

| Arm | RTT | Setup | Agent | **Total** | Turns | Output tokens | Cost | Wire |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| perry (MCP) | 0 ms | 0 ms | 74.9 s | **77.6 s** | 6 | 15,040 | $0.33 | 63.5 KB |
| git shallow | 0 ms | 4.7 s | 16.5 s | **23.6 s** | 4 | 585 | $0.07 | 3.39 MB |
| git sparse, told the dir | 0 ms | 2.3 s | 15.9 s | **20.6 s** | 4 | 727 | $0.08 | 769 KB |
| perry (MCP) | 75 ms | 0 ms | 75.8 s | **78.2 s** | 6 | 15,070 | $0.33 | 63.8 KB |
| git shallow | 75 ms | 5.0 s | 19.4 s | **27.0 s** | 4 | 565 | $0.07 | 3.39 MB |
| git sparse, told the dir | 75 ms | 2.8 s | 14.5 s | **19.6 s** | 4 | 623 | $0.07 | 769 KB |

Perry took 3.3 to 4.0 times longer, at 4.6 times the cost, while moving 53
times fewer bytes. It was not noise: the three perry trials at 0 ms landed
within 1.8 s of each other and their output token counts within 126.

The agent had to edit `balancer/rls/balancer.go`, 27,314 bytes and 733 lines,
and `perry_commit` took each file's **complete new contents**. So the model
re-emitted the whole file: 15,040 output tokens against the git arm's 585,
which used `Edit` and sent a few lines. The arithmetic holds up -- both arms
carry about 15 s of fixed turn overhead, and perry's extra 59 s buys 14,455
extra output tokens at roughly 245 tokens per second, which is what this model
does. The latency was not transport, parsing or SQLite. It was the model
typing out a file it had just been shown.

That design was deliberate and argued for in the commit that introduced the
adapter: a model emitting a unified diff against a file it read is the most
common way an agent's edit silently fails to apply. The reliability reasoning
still stands. The claim about cost attached to it was asserted without
measurement, and measured, it was wrong.

### The fix, and what changed

`POST /v1/repos/{repo}/commit` and `perry_commit` now take `edits`: exact
search-and-replace pairs against the file already on the branch. The
reliability argument is kept rather than abandoned, because search-and-replace
is not a diff. A unified diff carries line numbers and context the model
reconstructs from memory, and a wrong one can still apply, in the wrong place.
An exact string occurs once or it does not: a match count other than one is
refused by the server and reported back to the model with the count.

Nothing else changed between the two runs -- same prompts, same corpus, same
model, same harness, same machine. Output tokens fell from 15,040 to 1,078 and
the arm that was 54 s behind came out 7 s ahead.

### What perry wins, and what it still loses

Wins, in order of how much they mattered:

- **Setup is free.** 0 ms against 2.3 to 4.9 s, in every run.
- **Latency barely touches it.** 0 ms to 75 ms of RTT costs perry 1.2 s and the
  shallow clone 3.4 s, because perry makes four requests against git's five to
  nine.
- **Bytes.** 28 KB against 3.39 MB. Real, and the thing that mattered least
  here; it is a fleet and bad-link argument, not a wall-clock one.

It still loses on cost: $0.134 against $0.071. Output tokens are now close,
1,078 against 551, so the gap is prompt rather than generation. Perry's arm
takes 6 turns to git's 4 and carries seven tool schemas in a prefix re-read
every turn: 215k cached input tokens against 154k, and 25k of cache writes
against 12k. Perry buys wall clock and bandwidth with context.

### What this still does not show

- One task, one repository, one model, one file size. The first run's gap was
  a function of edited-file size; the second run's is smaller and could also
  move with it. The task was not chosen to favour either side, but it was not
  varied either.
- Token counts are not cleanly comparable between arms. Each carries a
  different fixed system prompt, so only the difference between arms means
  anything, not the absolute figures.
- The git arms use tools this model has seen endlessly in training; the perry
  arm gets seven it has never seen. That cuts against perry and cannot be
  corrected for here.
- Two runs reached for PowerShell, which was not in their allowed list, and
  adapted. Tool-set fit is a confound in both directions.
- The edit here is an append, so the `old` anchor is the end of the file. A
  change in the middle of a dense file is the case where an exact anchor is
  hardest to make unique, and it is not measured.

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
  file size, at three trials per cell. The first run of it, with whole-file
  writes, turned entirely on the edited file's size, and that was not varied;
  there is no reason to assume the second run does not turn on it too.
- The real-agent comparison edits by appending, so the anchor an edit has to
  match is the end of the file. Matching uniquely in the middle of a dense
  file is the harder case and is not measured.
- The contention benchmark's HTTP runs are loopback. A remote agent would also
  pay network latency on each of the three requests per attempt, and a remote
  git agent would pay it per subprocess round trip too.
- Git's clones in the contention benchmark happen before the clock starts.
  Including them would widen the gap.
