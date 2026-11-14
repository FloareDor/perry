# Perry

[![CI](https://github.com/FloareDor/perry/actions/workflows/ci.yml/badge.svg)](https://github.com/FloareDor/perry/actions/workflows/ci.yml)
[![Go Reference](https://img.shields.io/badge/go-reference-00ADD8?logo=go&logoColor=white)](https://pkg.go.dev/github.com/perry-dev/perry)
[![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-00ADD8)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Status: alpha](https://img.shields.io/badge/status-alpha-yellow)](#project-status)

Perry is a git server for coding agents. It serves clone, push and fetch over the git
wire protocol, and it serves an HTTP API that lets an agent grep a repository, read one
file at one ref, fork a branch and commit to it without cloning anything.

Perry implements the protocol in Go rather than shelling out to the `git` binary. Objects
and refs live in one SQLite file per repository. A TypeScript coordinator runs a fleet of
agents against it.

## Project status

Alpha. Every layer is cross-checked against git 2.35: packs Perry writes pass
`git index-pack`, a repository pushed in and cloned back out is byte-identical and
`git fsck` clean, and twenty stock git clients pushing at once all land.

| Package | Scope | Status |
|---|---|---|
| `internal/object` | Object model, SHA-1 addressing, loose encoding | Complete |
| `internal/pack` | Packfile reader, delta codec, pack writer, v2 index | Complete |
| `internal/protocol` | pkt-line, `upload-pack`, `receive-pack` | Complete |
| `internal/server` | Smart HTTP: clone, fetch, push | Complete |
| `internal/store` | SQLite backend, compare-and-swap refs | Complete |
| `internal/store` | S3 backend | Not started |
| `internal/api` | grep, file, tree, fork, commit, ref | Complete |
| `internal/mcp` | Model Context Protocol adapter | Complete |
| `coordinator/` | TypeScript agent fleet runner | Complete |

Not yet done: an S3 backend and background repacking. Run it on a laptop or a single
VM; do not put production data behind it yet.

## Why use Perry?

**Most agent tasks touch a handful of files.** Twenty agents cloning a 500 MB repository
move 10 GB before any of them has read a line. Perry serves grep and file-at-ref over
HTTP against the object store, so an agent reads what it needs and skips the checkout.

**Concurrent pushes do not queue.** Packs are immutable, so a push writes a new one
rather than locking an existing one. The only serialization point is a compare-and-swap
on a single ref row, so twenty agents on twenty branches never contend.

**Forks are free.** Branching a repository for a new agent writes one ref and copies no
object data.

**It is still git.** `git clone`, `git push` and `git fetch` work against it with the
stock client, so everything downstream of the repository keeps working.

## Why not use Perry?

**It is young.** One author, one corpus, one machine. See [Project status](#project-status).

**It costs more per task than a checkout does.** Perry finishes a real agent task
sooner than an optimised git checkout and moves far fewer bytes, but at roughly 1.9
times the token cost: its tools take more turns and ride in a larger prompt that is
re-read every one of them. If you are paying for inference and not for bandwidth or
wall clock, a shallow clone is cheaper.

**It is a storage and protocol layer, and will stay one.** No web UI, pull
requests, CI or code review. Smart HTTP only: no SSH transport, no Git LFS, no GitHub
mirroring.

**Its authentication is one shared token.** One token covers every repository and every
route, sent in clear text over Basic auth unless you terminate TLS in front of it. There
are no users, no per-repository scopes and no revocation. That is enough for a fleet of
agents inside one trust boundary and not enough for anything else.

**It does not serve shallow or partial clones.** `--depth` is refused and `--filter` is
ignored, so a client that wants a checkout gets the whole history. An agent that needs
a real working tree, to run a build or a test suite, pays more against perry than
against a host that supports both. The read API exists so that most agents do not need
one.

**If one team of humans shares one repository, use anything else.** The design assumes
many short-lived branches written by machines.

## Installation

```bash
go install github.com/perry-dev/perry/cmd/perry@latest
perry serve -data ./data
```

The server listens on `127.0.0.1:8080`, keeps one SQLite file per repository under
`-data`, and needs no other process. Pushing to a name that does not exist creates it:

```bash
git remote add perry http://localhost:8080/app.git
git push perry main
```

### Exposing it to a network

Git smart HTTP carries `receive-pack`, so a perry server without a token accepts
anonymous **pushes** to any repository, and creates repositories on demand. Set a token
before binding anything but loopback; perry refuses to start otherwise.

```bash
perry serve -addr 0.0.0.0:8080 -token "$PERRY_TOKEN"
```

The token is required on every route — git and API alike — as either an
`Authorization: Bearer` header or the password in HTTP Basic auth. Git cannot send a
bearer token, so it uses Basic; the username is ignored:

```bash
git clone http://x:"$PERRY_TOKEN"@host:8080/app.git
curl -H "Authorization: Bearer $PERRY_TOKEN" http://host:8080/v1/repos/app/refs
```

`/healthz` stays open so a liveness probe needs no credential. There is one token for
the whole server: no per-repository access, no per-user identity, no revocation. Put it
behind TLS, because Basic auth sends the token in clear text.

Or build from source:

```bash
git clone https://github.com/perry-dev/perry
cd perry
make test     # the suite, without the slow benchmarks
make build    # writes ./perry
make help     # every target
```

## Examples

Search a repository without cloning it:

```bash
curl 'http://localhost:8080/v1/repos/app/grep?q=func+Resolve&ref=main&limit=20'
```

```json
{"matches":[{"path":"internal/api/api.go","line":41,"text":"func Resolve(ctx context.Context, ..."}],
 "truncated":false,"filesScanned":400,"filesMatched":2}
```

Read one file at one revision, list files by glob, or list refs:

```bash
curl 'http://localhost:8080/v1/repos/app/file?ref=main&path=go.mod'
curl 'http://localhost:8080/v1/repos/app/files?ref=main&glob=internal/**/*.go'
curl 'http://localhost:8080/v1/repos/app/refs?prefix=refs/heads/agent/'
```

Fork a branch for an agent, then commit to it with no working tree. `expect` is the
commit the branch must still point at, which turns the write into a compare-and-swap:

```bash
curl -X POST localhost:8080/v1/repos/app/fork -d '{"from":"main","branch":"agent/t-07"}'

curl -X POST localhost:8080/v1/repos/app/commit -d '{
  "branch":  "agent/t-07",
  "message": "t-07: add a note",
  "expect":  "3b73ec5c...",
  "changes": [{"path":"README.md","content":"<base64>"}]
}'
```

A change can carry `edits` instead of `content`, which replaces exact runs of text in
the file already on the branch. An agent changing one line of a large file sends that
line, not the file:

```bash
curl -X POST localhost:8080/v1/repos/app/commit -d '{
  "branch":  "agent/t-07",
  "message": "t-07: raise the timeout",
  "expect":  "3b73ec5c...",
  "changes": [{"path":"server.go","edits":[
    {"old":"timeout := 30 * time.Second","new":"timeout := 90 * time.Second"}
  ]}]
}'
```

Each `old` must occur exactly once, or the commit is refused and names the count:
an edit that could land in two places is rejected rather than guessed at. Set `all`
to replace every occurrence on purpose. Edits apply in order, each to the result of
the last, and the file keeps its mode.

A losing `expect` returns 409 and the agent retries on the fresh tip. Moving a ref to
something that is not a fast-forward is refused unless `force` is set.

Run a fleet of agents from TypeScript. Each one forks, greps, reads only what matched,
and commits; a serial queue then replays each branch onto `main`:

```bash
cd coordinator && npm install
node --experimental-strip-types src/index.ts --repo app --tasks tasks.json --agents 20
```

```
fleet: 20 tasks, 20 at a time, scripted agent
  t-01   38ms  9f1c0aa  appended a note to internal/api/api.go
  ...
20/20 succeeded, 20 merged, in 212ms
140 API requests, 3.1 KB read, 0 clones
```

`--mode claude` routes each task through the Claude Agent SDK instead of the scripted
edit. See `coordinator/README.md`.

### Connecting a coding agent

`perry mcp` bridges one repository to any agent that speaks the [Model Context
Protocol](https://modelcontextprotocol.io). It runs on stdio, which is how every MCP
client launches a local server, so connecting an existing agent is a few lines of its
configuration rather than a change to how you work:

```json
{
  "mcpServers": {
    "perry": {
      "command": "perry",
      "args": ["mcp", "-server", "http://perry.internal:8080", "-repo", "app"],
      "env": { "PERRY_TOKEN": "..." }
    }
  }
}
```

For Claude Code specifically:

```bash
claude mcp add perry --env PERRY_TOKEN=... -- perry mcp -server http://perry.internal:8080 -repo app
```

Seven tools come with it:

| Tool | What the agent gets |
| --- | --- |
| `perry_grep` | a regex search of every file at a revision, matches only |
| `perry_read_file` | one file's exact bytes, verbatim and unnumbered |
| `perry_list_files` | the paths at a revision, filtered by a glob |
| `perry_list_tree` | one directory, with types and sizes |
| `perry_list_branches` | branches and their tips, for `expect` |
| `perry_create_branch` | a branch, as a pointer write |
| `perry_commit` | a commit, from edits or whole-file contents, under compare-and-swap |

The adapter is a client of perry's HTTP API, not of the object store, so the perry
server can be on another host. That is the point: the agent searches, reads, edits and
commits without a clone, a checkout, a staging area or a push, and a plain `git clone`
of the branch it wrote returns a normal repository.

It does not shell out to `git`, so an agent given these tools has no local repository
to fall back on.

It does not take a unified diff either. `perry_commit` takes search-and-replace edits:
exact text, matched once, or refused. A diff carries line numbers and context a model
has to reconstruct from memory, and when it gets them wrong the patch can still apply,
in the wrong place. An exact string either occurs once or it does not, so the ambiguity
is rejected at the server rather than resolved by guessing.

```bash
perry mcp -server http://perry.internal:8080 -repo app   # $PERRY_TOKEN for auth
```

The packfile layer is usable as a library:

```go
r, _ := pack.NewReader(f, size)
byID, _ := r.BuildOffsetIndex()
for id, offset := range byID {
    obj, _ := r.ObjectAt(offset)
    fmt.Printf("%s  %-6s  %d bytes\n", id.Short(8), obj.Type, obj.Size())
}

var buf bytes.Buffer
name, err := pack.WriteAll(&buf, objects) // git index-pack accepts the result
```

## Benchmarks

AMD Ryzen 7 5800H, Windows 11, Go 1.26. Reproduce with:

```bash
make corpora   # three real repositories into testdata/, about 50 MB, cached
make bench     # about ten minutes
```

[BENCHMARKS.md](BENCHMARKS.md) has the full method, every baseline and the known
limits. These numbers are one machine.

### Agents

What an agent pays to start a task: find the file containing a given symbol and have
its contents in hand. The corpus is [grpc/grpc-go](https://github.com/grpc/grpc-go) at
full depth, 6100 commits and 1424 files.

Every path runs over real HTTP and every byte is counted by the same proxy, so "wire"
means one thing in every row. An earlier version of this table read git's transfer off
`git count-objects -v`, which measures what git wrote to disk rather than what crossed
the network, and compared it against the length of perry's HTTP responses. Those are
different quantities, and the numbers below are smaller for it.

| Setup path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone, then `git grep` | 5.55 s | 44.44 MB | 60.67 MB |
| shallow clone `--depth 1` | 4.77 s | 3.37 MB | 18.11 MB |
| shallow + `--filter=blob:none` | 5.48 s | 3.46 MB | 18.28 MB |
| shallow + sparse, directory known | 2.23 s | 746 KB | 3.08 MB |
| warm worktree, repository on disk | 1.53 s | 0 B | 14.53 MB |
| **grep over the API, read the file** | **728 ms** | **27.2 KB** | **0 B** |

Against a full clone that is about 1,600 times fewer bytes. Against the best git path
that still transfers anything — a shallow clone with a sparse checkout, which has to be
told which directory to look in — it is roughly 3 times faster and moves about 27 times
fewer bytes, while finding the file on its own.

That is code *access*, not a coding task. With a real model in the loop the ordering
reverses; see [A real agent](#a-real-agent-with-a-model-in-the-loop) below.

The warm worktree is the honest comparison and it transfers nothing at all, because its
objects are already local. Perry does not beat it on bytes and never will; what it does
is reach the same answer without the clone that put those objects there, and without
leaving 14.53 MB on disk.

A blind `blob:none` grep is the slowest path on the board, because lazy blob fetch
defeats a search that does not already know its answer. Server-side grep is the
structural difference, not the transport.

Pushing a repository into perry is paid once: 1.1 s for cobra, 22.2 s for grpc-go.

### The whole task, with latency

A faster git operation is not a faster agent, so this measures the whole unit of work —
locate the symbol, read its file, edit it, publish the edit — from nothing on disk to
the change being durable on the server. Agents do not run next to their git host, so the
same proxy adds a fixed delay to every request on both sides.

grpc-go, time to finish the task:

| Path | 0 ms | 25 ms | 75 ms | Wire | Requests |
|---|---:|---:|---:|---:|---:|
| full clone | 5.99 s | 6.34 s | 6.38 s | 44.46 MB | 5 |
| shallow `--depth 1` | 5.58 s | 5.85 s | 5.98 s | 3.39 MB | 5 |
| shallow + sparse, directory known | 2.97 s | 3.02 s | 3.59 s | 769 KB | 9 |
| warm worktree + fetch | 3.03 s | 3.05 s | 2.99 s | 45.9 KB | 4 |
| **perry API** | **427 ms** | **617 ms** | **718 ms** | **63.3 KB** | **4** |

Latency does not change the ordering, and it does not flatter perry either: every path
pays round trips × latency, and perry's four requests against git's four to nine means
the *ratio* narrows as the link gets worse — 7× down to 5× against the sparse checkout —
even as the absolute gap grows from 2.54 s to 2.87 s. Perry's advantage here is work
avoided, not round trips saved.

What this does not show is a real agent finishing a real task sooner. The edit is
scripted, it is one agent, and no model is in the loop. That measurement is next, and
the first time it ran, Perry lost.

### A real agent, with a model in the loop

The same task, the same model, once with only Perry's MCP tools and once with a shell
and an optimised checkout. `claude-sonnet-5-5` pinned on both sides, grpc-go, three
trials per cell, all 18 runs completed the task and completion is verified by reading
the edit back off the server.

| Arm | RTT | Setup | Agent | Total | Turns | Output tokens | Cost | Wire |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| **perry (MCP)** | 0 ms | **0 ms** | 14.2 s | **16.8 s** | 6 | 1,078 | $0.13 | **28.1 KB** |
| git shallow | 0 ms | 4.7 s | 16.6 s | 23.8 s | 4 | 551 | $0.07 | 3.39 MB |
| git sparse, told the dir | 0 ms | 2.3 s | 14.9 s | 20.0 s | 4 | 628 | $0.07 | 769 KB |
| **perry (MCP)** | 75 ms | **0 ms** | 15.7 s | **18.0 s** | 6 | 1,191 | $0.13 | **28.4 KB** |
| git shallow | 75 ms | 4.9 s | 15.7 s | 23.1 s | 4 | 586 | $0.07 | 3.39 MB |
| git sparse, told the dir | 75 ms | 2.8 s | 15.0 s | 20.2 s | 4 | 614 | $0.07 | 769 KB |

Perry finishes the task **1.2 to 1.4 times sooner** while moving **27 to 121 times
fewer bytes**, and it still costs about **1.9 times as much**.

This is the second run of this benchmark. The first one is worth stating plainly,
because it is the only reason the second one looks like this.

**Perry lost the first run by three to four times**, at 4.6 times the cost: 77.6 s
against 23.6 s, with three trials agreeing within 1.8 s. The cause was a design
decision, argued for in a commit message and never measured. `perry_commit` took each
file's **complete new contents**, so a model editing a 733-line file re-emitted all 733
lines — 15,040 output tokens against 585 for the arm that had a normal edit tool. The
extra 59 s was the model typing out a file it had just been shown, at about 245 tokens
per second. A transport 53 times leaner is invisible next to a tool that makes the
model generate twenty-five times more text.

The fix was [search-and-replace edits](#the-http-api) in `POST /commit` and in
`perry_commit`. Nothing else changed: same prompts, same corpus, same model, same
harness. Output tokens fell from 15,040 to 1,078, and the arm that was 54 s behind came
out 7 s ahead.

What Perry wins, and why:

- **Setup is free.** 0 ms against 2.3 to 4.9 s, in every run. The agent's first tool
  call does real work; git's first few minutes of wall clock are a download.
- **Latency barely touches it.** 0 ms to 75 ms RTT costs Perry 1.2 s and the shallow
  clone 3.4 s, because Perry makes 4 requests against git's 5 to 9.
- **The bytes are real but were never the bottleneck.** 28 KB against 3.39 MB matters
  for a fleet and for a laptop on hotel wifi. It is not what made the difference here.

What it still loses on is **cost: $0.13 against $0.07**. Output tokens are now close,
551 against 1,078, so the gap is prompt: Perry's arm takes 6 turns to git's 4, and
carries seven tool schemas in a prefix that is re-read every turn — 215k cached input
tokens against 154k. Perry buys wall clock and bandwidth with context.

One task, one repository, one model, one file size. See
[BENCHMARKS.md](BENCHMARKS.md#a-real-agent-with-a-model-in-the-loop) for every run and
the confounds.

### Contention

Every agent appends its own line to one file on one branch, and the test counts the
lines that survived. Perry reads the tip, reads the file and commits with a
compare-and-swap, retrying on a 409; the git agents push and, on rejection, reset to
the new tip and retry. Both go over HTTP. The git clones happen before the clock
starts, which is generous to git.

| Agents | Perry over HTTP | Retries each | Git | Retries each |
|---:|---:|---:|---:|---:|
| 1 | 4 ms | 1.0 | 494 ms | 1.0 |
| 5 | 33 ms | 1.8 | 3.84 s | 3.0 |
| 20 | 146 ms | 1.6 | 28.93 s | 10.5 |
| 50 | 393 ms | 2.3 | 2 m 26 s | 24.4 |

No lost updates on either side at any size. Git is correct here, just expensive.

The one-agent row is the control: nobody contends, so it is fixed overhead alone —
process spawning and working-tree writes against one HTTP round trip. That is already
135x, and it is not a claim about concurrency. What contention adds on top is the
growth from 135x to 371x.

The retry counts are the design result. Git's grow linearly with the number of agents,
so total work grows quadratically: at fifty agents each one pushes 24.4 times to land
once. Perry's stay near flat at 2.3, because a losing writer re-reads one ref row
rather than re-running a fetch, a reset and a push.

Perry over HTTP matches perry in process to within noise here, so serialisation is not
hiding anything. Both are loopback: a remote agent would additionally pay network
latency on each of the three requests per attempt.

### Operations

Individual operations, against a generated corpus of 400 files and 7.49 MB. These are
microbenchmarks of one call at a time, not a comparison against git.

| Operation | Result |
|---|---|
| Fork a branch | 171 µs, 0 objects copied |
| 20 stock git clients pushing at once | 20/20 landed in 709 ms, no lost refs |
| 40 concurrent pushes, SQLite backend | all landed, no lost refs |
| grep, rare pattern, 400 files | 122 ms |
| grep, common pattern, 400 files, limit 50 | 18 ms |
| Fleet of 5 agents, fork + grep + read + commit + merge | 106 ms, 35 requests, 0.8 KB, 0 clones |

Grep runs one worker per core and stops reading once it has four times the requested
limit, so the common-pattern case finishes faster than the rare one.

### Throughput

| Operation | Throughput |
|---|---|
| Resolve a packfile, deltas included | 13.2 MB/s |
| Apply a delta | 6.36 GB/s |
| Search for a delta | 159 MB/s |

Applying is two orders of magnitude faster than searching, and a delta is searched for
once but applied on every read.

### Compression

Perry's packer against git's own output for 1078 objects: a shallow bare clone of
[spf13/cobra](https://github.com/spf13/cobra), 10.19 MB uncompressed, 69% of pack
entries stored as deltas. Objects are sorted by type, then by a hash of the path they
were first seen at, then by size, which is git's order; the delta window is how many
recently written objects each new object is compared against.

| Window | Pack size | vs. uncompressed | vs. git | Pack time |
|---:|---:|---:|---:|---:|
| 0 (off) | 2.63 MB | 3.9x | 5.02x | 0.30 s |
| **10** (default) | **456 KB** | **22.9x** | **0.85x** | **1.55 s** |
| 25 | 440 KB | 23.7x | 0.82x | 5.36 s |
| 50 | 434 KB | 24.1x | 0.81x | 13.1 s |
| 100 | 433 KB | 24.1x | 0.81x | 29.2 s |
| 250 | 431 KB | 24.2x | 0.80x | 66.6 s |

```
Pack size by delta window                     git's own pack: 537 KB
  0  ████████████████████████████████████████████████  2.63 MB
 10  ████████                                           456 KB
 25  ████████                                           440 KB
 50  ████████                                           434 KB
100  ████████                                           433 KB
250  ████████                                           431 KB
     └────────────────────────────────────────────────
```

Ordering keeps the window small. Sorted by type and size alone, the same
objects at window 10 pack to 2.20 MB, 4.2x git, and need a window of 250 to catch up.
With revisions of one file next to each other, the right base is nearly always within
ten slots, and a wider window buys 5% for forty times the packing time.

Why Perry's pack comes out smaller than git's here is not settled. Git's pack was
written by a server answering a shallow clone, and a server reuses the deltas it
already has rather than searching for better ones.

## How it works

```
git clone / push / fetch ────┐
   smart HTTP, stock client  ├──▶  Perry
                             │      │
GET  /v1/repos/{r}/grep ─────┤      ├── objects ──▶  SQLite, one file per repo
GET  /v1/repos/{r}/file ─────┤      │               (S3 planned)
GET  /v1/repos/{r}/files ────┤      │
POST /v1/repos/{r}/fork ─────┤      └── refs ─────▶  one row per ref, compare-and-swap
POST /v1/repos/{r}/commit ───┤
POST /v1/repos/{r}/ref ──────┘
```

Objects are written before any ref moves, and refs move only through a compare-and-swap
batch, so a reader never sees a ref that points at something missing and two writers
never overwrite each other. A push is a new immutable pack plus one ref batch; twenty
branches touch twenty rows. A fork is one row.

Commits made through the API are built from blobs up: the changed files become blobs,
every tree on the path from each file to the root is rewritten, and a commit object is
written on top. Git trees are hashed by content, so a change to one file in a deep
directory rewrites only the trees on that path, not the repository.

Reading is where the pack format gets awkward. Pack entries sit back to back with no
separator, so finding entry k+1 means decompressing entry k and knowing exactly where
its stream ended. Deflate's worst case is bounded at five bytes per 65535 bytes of
input, so Perry sizes the read in advance and each entry costs a single `ReadAt`.

Delta chains are bounded at depth 100 on read. A corrupt or hostile pack can describe a
cycle, and an unbounded resolver would recurse until the process died.

## Testing

```bash
make test     # the suite, about twenty seconds
make bench    # the comparative benchmarks, about ten minutes
```

Everything in `internal/bench` is opt in, because together the benchmarks run past the
default `go test` timeout. `go test ./...` skips them and stays green; `make bench`
sets `PERRY_BENCH=1` and a longer timeout. Nothing in that package asserts a
correctness property that is not also covered in `internal/api`, `internal/pack` or
`internal/server`.

Every layer is checked against the git binary, because a reimplementation that agrees
only with itself proves nothing.

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

The MCP adapter is tested as a protocol, not as a set of helpers: a session is driven
over real pipes with newline-delimited JSON-RPC, and the end-to-end test walks the
whole agent loop, locate to read to branch to commit, then reads the result back out of
the store rather than through the path that wrote it. Separate tests pin down the parts
that fail quietly: version negotiation, a tool failure arriving as a tool result rather
than a transport error the model cannot see, a stale `expect` being refused, and
notifications drawing no reply so the response stream never shifts by one.

Corruption gets the same attention as correctness: bad magic, wrong version, truncated
streams, flipped bytes in the body and in the trailer, reserved delta opcodes, copies
running past the end of a base, and declared sizes that disagree with the payload.

## Launch video

[Watch it](https://github.com/FloareDor/perry/releases/download/v0.1.0/launch.mp4) (60 seconds).

`video/` is the Remotion project that renders it. There is no narration; the edit is cut
to a 100 BPM bed that `video/scripts/music.py` synthesises, so every scene starts on a
downbeat. `npm install && npm run render` in that directory writes `out/launch.mp4`, and
`video/README.md` has the tempo grid.

## Comparison

|  | Native git protocol | Cloneless read API | Agent fleets | Open source |
|---|:---:|:---:|:---:|:---:|
| [Pierre code.storage](https://code.storage) | Yes | Yes | Yes | No |
| [Entire](https://entire.io) | Uses yours | No | Client-side | Yes |
| [Gitea](https://gitea.io) | Via `git` | No | No | Yes |
| [Soft Serve](https://github.com/charmbracelet/soft-serve) | Via `git` | No | No | Yes |
| [go-git](https://github.com/go-git/go-git) | Library only | No | No | Yes |
| [Gitaly](https://gitlab.com/gitlab-org/gitaly) | Via `git` | Internal gRPC | No | Yes |
| Perry | From scratch | Yes | Yes | Yes |

Pierre's code.storage is the closest thing to Perry and a far more mature product. Perry
exists because there is no open implementation of the same idea.

## Related

- [`gitformat-pack`](https://git-scm.com/docs/gitformat-pack) and
  [`gitprotocol-http`](https://git-scm.com/docs/gitprotocol-http) are the real
  specification.
- [go-git](https://github.com/go-git/go-git) is the mature pure-Go git library. Use it if
  you want a client.
- [Gitaly](https://gitlab.com/gitlab-org/gitaly) is GitLab's git storage server, and the
  closest thing to prior art on the server side.

## License

MIT. See [LICENSE](LICENSE).
