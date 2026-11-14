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
| perry (MCP) | 0 ms | 0 ms | 16.6 s | **19.0 s** | 6 | 1,223 | $0.092 | **1.6 KB** | 4 |
| git shallow | 0 ms | 4.7 s | 17.3 s | **24.2 s** | 4 | 596 | $0.071 | 3.39 MB | 5 |
| git sparse, told the dir | 0 ms | 2.3 s | 14.5 s | **19.1 s** | 4 | 679 | $0.072 | 769 KB | 9 |
| perry (MCP) | 75 ms | 0 ms | 15.2 s | **17.7 s** | 6 | 1,200 | $0.087 | **1.5 KB** | 4 |
| git shallow | 75 ms | 4.9 s | 16.1 s | **23.3 s** | 4 | 575 | $0.071 | 3.39 MB | 5 |
| git sparse, told the dir | 75 ms | 2.8 s | 14.3 s | **19.5 s** | 4 | 654 | $0.072 | 769 KB | 9 |

Read against the two git paths separately, because they say different things.

Against the **shallow clone**, perry finishes 1.27 to 1.32 times sooner and
moves 2,100 times fewer bytes, at 1.24 to 1.30 times the cost.

Against the **sparse checkout** -- which is told which directory to look in,
and so is git on ground it would not have in practice -- perry is a dead heat
at 0 ms, 19.0 s against 19.1 s, and 1.10 times faster at 75 ms. The honest
summary of this task is that perry beats a realistic git setup comfortably and
ties git's best case, while moving 480 times fewer bytes than that best case.

One task at three trials per cell establishes that the ordering can go perry's
way, not that it generally does. The [twenty-task suite](#twenty-tasks-not-one)
is the wider measurement.

Every run, because an LLM's wall clock is not repeatable:

| Arm | RTT | # | Setup | Agent | Total | Turns | Out tok | Cost | Wire |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| perry | 0 ms | 1 | 0 ms | 17.69 s | 20.31 s | 6 | 1,300 | $0.0929 | 1.6 KB |
| perry | 0 ms | 2 | 0 ms | 16.58 s | 19.02 s | 6 | 1,223 | $0.0919 | 1.6 KB |
| perry | 0 ms | 3 | 0 ms | 16.44 s | 18.96 s | 6 | 1,195 | $0.0914 | 1.5 KB |
| shallow | 0 ms | 1 | 4.65 s | 15.63 s | 22.73 s | 4 | 571 | $0.0706 | 3.39 MB |
| shallow | 0 ms | 2 | 4.58 s | 17.26 s | 24.16 s | 4 | 596 | $0.0707 | 3.39 MB |
| shallow | 0 ms | 3 | 4.66 s | 18.03 s | 25.43 s | 5 | 727 | $0.0811 | 3.39 MB |
| sparse | 0 ms | 1 | 2.32 s | 14.47 s | 19.34 s | 4 | 655 | $0.0721 | 769 KB |
| sparse | 0 ms | 2 | 2.31 s | 14.21 s | 19.01 s | 4 | 687 | $0.0723 | 769 KB |
| sparse | 0 ms | 3 | 2.18 s | 14.51 s | 19.09 s | 4 | 679 | $0.0723 | 769 KB |
| perry | 75 ms | 1 | 0 ms | 17.68 s | 20.04 s | 7 | 1,419 | $0.0952 | 1.6 KB |
| perry | 75 ms | 2 | 0 ms | 15.18 s | 17.67 s | 6 | 1,200 | $0.0874 | 1.5 KB |
| perry | 75 ms | 3 | 0 ms | 14.52 s | 16.90 s | 5 | 1,134 | $0.0861 | 1.4 KB |
| shallow | 75 ms | 1 | 4.87 s | 20.25 s | 27.45 s | 5 | 770 | $0.0810 | 3.39 MB |
| shallow | 75 ms | 2 | 4.86 s | 16.06 s | 23.31 s | 4 | 575 | $0.0704 | 3.39 MB |
| shallow | 75 ms | 3 | 5.00 s | 15.55 s | 23.05 s | 4 | 599 | $0.0707 | 3.39 MB |
| sparse | 75 ms | 1 | 2.71 s | 14.43 s | 19.60 s | 4 | 622 | $0.0713 | 769 KB |
| sparse | 75 ms | 2 | 2.76 s | 14.30 s | 19.49 s | 4 | 654 | $0.0717 | 769 KB |
| sparse | 75 ms | 3 | 2.79 s | 14.12 s | 19.14 s | 4 | 640 | $0.0719 | 769 KB |

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

### Chasing the cost gap: three changes, one that worked

Perry won on wall clock and lost on cost, 1.9 times git's. The obvious
explanation was turns -- six against git's four, each one re-reading the whole
prompt prefix -- so the obvious fix was to need fewer of them. Three changes
were measured separately, three trials each, at 0 ms RTT, on grpc-go.

| | Turns | Requests | Cost | Wire | Total |
|---|---:|---:|---:|---:|---:|
| baseline | 6 | 4 | $0.1338 | 28.1 KB | 17.8 s |
| A: identical prompts | 6 | 4 | $0.1338 | 28.1 KB | 17.8 s |
| B: fewer round trips | 5 | 3 | $0.1346 | 29.8 KB | 16.9 s |
| C: ranged reads | 6 | 4 | **$0.0924** | **1.5 KB** | 19.6 s |
| *git shallow, for scale* | *4* | *5* | *$0.0706* | *3.39 MB* | *23.4 s* |

**A fixed a confound in the harness, and changed nothing.** The two arms were
not being told the same thing: perry's prompt said "1. Find the file. 2. Read
that file. 3. Append... 4. Commit", and git's said "1. Find the file.
2. Append... 3. Commit". Perry's arm had been handed an extra step for two
full runs without anyone noticing. Removing it moved neither the turn count nor
the cost, because the model read the file either way -- it needed to. The
confound was real and worth removing; it was not the cause of anything.

**B did what it was supposed to and still did not help.** `perry_grep` now
reports the commit it searched, which removes the `perry_list_branches` call
that existed only to learn a tip the search already knew, and it can return the
matched files whole, which removes the `perry_read_file` call. Requests fell
from four to three and turns from six to five, exactly as intended. Cost did
not move. Returning the file whole put all 733 lines into the context one turn
earlier instead of one turn later, and the saved turn was worth about as much
as the extra context cost.

**C was the one.** `perry_read_file` could only return a whole file, up to
400,000 characters of it. The git arm could run `tail`. That asymmetry, not the
turn count, was the cost: an agent appending one line was reading 27 KB to find
out what the last line was. Giving the tool a line range -- `start_line` and
`end_line`, with a negative start counting back from the end -- cut cost by 31%
and the bytes on the wire by 95%, to 1.5 KB for the whole task. It costs about
two seconds of wall clock, because the model now makes a small read it used to
fold into a big one.

The lesson is not about perry. Turns were the visible difference and the wrong
thing to optimise; what mattered was how much of the repository ends up in the
model's context, which is the one resource an agent actually spends. A tool
that cannot return part of a file forces the whole file into context, and no
amount of combining calls fixes that.

Perry still costs 1.31 times git here, down from 1.9. What is left is the seven
tool schemas in a prefix re-read every turn, and two more turns than git takes.

### What perry wins, and what it still loses

Wins, in order of how much they mattered:

- **Setup is free.** 0 ms against 2.2 to 5.0 s, in every run. This is the
  whole of perry's margin against the shallow clone, and it is the one
  advantage that cannot be competed away by a cleverer clone: there is
  nothing to download.
- **Bytes.** 1.5 KB against 3.39 MB, once the agent could read a line range
  instead of a whole file. That is 2,100 times less. It is also the thing
  that mattered least to the clock; it is a fleet and bad-link argument, not
  a wall-clock one.
- **Latency is cheap but not free.** Perry makes four requests against git's
  five to nine, so 75 ms of RTT moves it less than it moves a clone. At one
  trial per cell this benchmark cannot separate that from noise, and the
  earlier claim that it cost perry 1.2 s against the clone's 3.4 s was
  reading more into three runs than three runs support.

It still loses on cost: $0.092 against $0.071, down from 1.9 times to 1.30.
Output tokens are close, 1,223 against 596, so what is left is prompt rather
than generation: perry's arm takes six turns to git's four and carries seven
tool schemas in a prefix re-read on every one of them. Perry buys wall clock
and bandwidth with context, at a better rate than it used to.

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

## Twenty tasks, not one

Everything above turns on a single task: append one line to the end of one
file. That is the easiest edit there is -- no anchor to find, nothing
ambiguous, nothing to understand first -- and a result that rests on it says
less than it appears to.

So: ten task shapes across both corpora, one trial each, at 0 ms RTT. Each is
posed to all three arms in identical words, and checked by reading the finished
repository back through a path the agent did not write through. Several have an
answer the harness computes from the repository before the agent ever sees it
-- a package name, a file's length, how many files match -- so that "the marker
is present" cannot pass a task the agent only half did.

| Task | What it exercises |
|---|---|
| `append-end` | append a line to the end of a located file |
| `insert-above` | insert a line immediately above a located line |
| `two-files` | edit a located file and a named one in one commit |
| `read-then-answer` | report the package a located file declares |
| `count-matches` | report how many files contain a string |
| `length-branch` | choose between two answers by reading the file |
| `two-commits` | two commits in sequence, the second on top of the first |
| `new-file` | create a file that does not exist, in a new directory |
| `replace-all` | replace every occurrence of a string in one file |
| `top-and-bottom` | two edits to one file, at both ends |

### Completion first

| Arm | Finished | Median | Cost/task | Output tokens | Wire/task |
|---|---:|---:|---:|---:|---:|
| **perry (MCP)** | **20/20** | **18.3 s** | $0.0954 | 1,396 | **2.4 KB** |
| git shallow | 20/20 | 22.5 s | **$0.0804** | 760 | 1.81 MB |
| git sparse, told the dir | 19/20 | 21.1 s | $0.0844 | 905 | 596 KB |

Completion is the headline because a tool surface that is quick and gets the
task wrong is worth less than a slow one that does not. Two arms finished
everything. Perry is 1.15 times faster than the best git arm on the median
task, moves 248 to 754 times fewer bytes, and costs 1.19 times as much -- a
narrower cost gap than the single task showed, because several of these tasks
need more reading and perry's reads are ranged.

### The one failure is the interesting row

`git sparse` got `count-matches` wrong on cobra: it answered 2 where the
repository has 3. It was not careless. A sparse checkout has only one
directory on disk, so the third matching file was not there to count. The arm
that downloads less cannot answer a question about what it did not download,
and nothing in its output marks the answer as partial.

This is the sparse arm's advantage and its cost in the same number. Perry
searches server-side, so a repository-wide question costs it one request
whether the answer is in the checkout or not -- there is no checkout.

### The margin tracks repository size

| Corpus | perry | git shallow | git sparse |
|---|---:|---:|---:|
| cobra, 1.6 MB packed | 18.2 s | 19.5 s | 21.2 s |
| grpc-go, 45.9 MB packed | 19.2 s | 25.5 s | 20.9 s |

On cobra perry and the shallow clone are within a second and a half of each
other, which is noise at one trial. On grpc-go the shallow clone is six
seconds behind. That is the shape to expect: perry's setup advantage is a
constant zero, and what it is being compared against grows with the
repository. On a small repository there is not much to save.

Perry is fastest in 6 of 10 tasks on each corpus, so the median hides real
spread. `new-file` on cobra went to the shallow clone by 4.5 seconds, and
`replace-all` went to the sparse checkout on both corpora.

### What this still does not show

- One trial per task. The completion column is solid at this sample -- a task
  either got done or did not -- but every timing here carries a single run's
  variance, and the per-task differences under about three seconds mean
  nothing.
- Two repositories, both Go, both libraries. Nothing here says what happens in
  a monorepo, or in a language whose files are much larger.
- Ten task shapes chosen by the author of the thing being measured. They were
  written to cover edit shapes that the single task did not, not to be won,
  but no one else has checked that.
- One model. The tool descriptions were written for a model to read, and a
  different model may read them differently.
- No task here needs a build or a test run. That is the case perry is worst
  at: it does not serve shallow or partial clones, so an agent that needs a
  real working tree pays more against perry than against GitHub.

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
  match is the end of the file. The twenty-task suite covers the harder
  shapes, but at one trial each.
- The twenty-task suite reports completion solidly and timings weakly: one
  trial per task means any per-task difference under about three seconds is
  noise. Its tasks were written by the author of the thing being measured.
- No agent benchmark here requires a build or a test run, which is the case
  perry is worst at.
- The contention benchmark's HTTP runs are loopback. A remote agent would also
  pay network latency on each of the three requests per attempt, and a remote
  git agent would pay it per subprocess round trip too.
- Git's clones in the contention benchmark happen before the clock starts.
  Including them would widen the gap.
