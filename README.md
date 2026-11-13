# Perry

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

**It is a storage and protocol layer, and will stay one.** No web UI, pull
requests, CI or code review. Smart HTTP only: no SSH transport, no Git LFS, no GitHub
mirroring.

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

The server listens on `:8080`, keeps one SQLite file per repository under `-data`, and
needs no other process. Pushing to a name that does not exist creates it:

```bash
git remote add perry http://localhost:8080/app.git
git push perry main
```

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
full depth, 6100 commits and 1424 files. The git baselines are served over `file://`,
which is faster than HTTP; perry is measured over HTTP, so the comparison is
conservative.

| Setup path | Time | Wire | Disk left |
|---|---:|---:|---:|
| full clone, then `git grep` | 4.07 s | 45.94 MB | 60.67 MB |
| shallow clone `--depth 1` | 5.00 s | 3.39 MB | 18.10 MB |
| shallow + `--filter=blob:none` | 5.43 s | 3.45 MB | 18.28 MB |
| shallow + sparse, directory known | 1.99 s | 737 KB | 3.08 MB |
| warm worktree, repository on disk | 1.79 s | 0 B | 14.53 MB |
| **grep over the API, read the file** | **956 ms** | **27.2 KB** | **0 B** |

Against a full clone that is 1690 times fewer bytes on the wire. Against the best git
path — a shallow clone with a sparse checkout, which has to be told which directory to
look in — it is about twice as fast and moves 27 times fewer bytes, while finding the
file on its own.

A blind `blob:none` grep is the slowest path on the board, because lazy blob fetch
defeats a search that does not already know its answer. Server-side grep is the
structural difference, not the transport.

Pushing a repository into perry is paid once: 1.3 s for cobra, 22.7 s for grpc-go.

### Contention

Every agent appends its own line to one file on one branch, and the test counts the
lines that survived. Perry commits with a compare-and-swap and retries on the losing
side; the git agents push and, on rejection, reset to the new tip and retry. The git
clones happen before the clock starts.

| Agents | Perry | Retries each | Git | Retries each |
|---:|---:|---:|---:|---:|
| 5 | 38 ms | 1.8 | 4.40 s | 3.0 |
| 20 | 130 ms | 2.0 | 29.55 s | 10.5 |
| 50 | 516 ms | 3.2 | 2 m 33 s | 24.0 |

No lost updates on either side at any size. Git is correct here, just expensive: its
retries grow linearly with the number of agents, so total work grows quadratically. A
losing perry writer re-reads one ref row instead of re-running a fetch, a reset and a
push.

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
