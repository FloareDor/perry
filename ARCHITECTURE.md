# How Perry works

Perry implements the git wire protocol in Go instead of shelling out to the `git`
binary. Objects and refs live in one SQLite file per repository. Nothing else runs.

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

## Writes

Objects are written before any ref moves, and refs move only through a
compare-and-swap batch. A reader never sees a ref pointing at something that is not
there yet, and two writers never overwrite each other.

A push is one new immutable pack plus one ref batch. Twenty agents on twenty branches
touch twenty rows and never queue behind each other, because packs are immutable: a
push writes a new one rather than locking an existing one. A fork is a single row.

Commits made through the HTTP API are built from blobs up. The changed files become
blobs, every tree on the path from each file to the root is rewritten, and a commit
object goes on top. Git trees are hashed by content, so changing one file in a deep
directory rewrites the trees on that path and nothing else.

## Reads

The pack format is where this gets awkward. Pack entries sit back to back with no
separator, so finding entry *k+1* means decompressing entry *k* and knowing exactly
where its stream ended. Deflate's worst case is bounded at five bytes per 65,535 bytes
of input, so Perry sizes the read in advance and each entry costs a single `ReadAt`.

Delta chains are bounded at depth 100 on read. A corrupt or hostile pack can describe
a cycle, and an unbounded resolver would recurse until the process died.

## Packing

Objects are sorted by type, then by a hash of the path they were first seen at, then
by size, which is git's order. The delta window is how many recently written objects
each new object is compared against; the default is 10.

Ordering is what keeps the window small. Sorted by type and size alone, the same
objects at window 10 pack to 2.20 MB against git's 537 KB, and need a window of 250 to
catch up. With revisions of one file next to each other, the right base is nearly
always within ten slots, and a wider window buys 5% for forty times the packing time.
The numbers are in [BENCHMARKS.md](BENCHMARKS.md#compression).

## Package layout

| Package | Scope |
|---|---|
| `internal/object` | Object model, SHA-1 addressing, loose encoding |
| `internal/pack` | Packfile reader, delta codec, pack writer, v2 index |
| `internal/protocol` | pkt-line, `upload-pack`, `receive-pack` |
| `internal/server` | Smart HTTP: clone, fetch, push |
| `internal/store` | SQLite backend, compare-and-swap refs |
| `internal/api` | grep, file, tree, fork, commit, ref |
| `internal/mcp` | Model Context Protocol adapter |
| `internal/bench` | Comparative benchmarks, opt in |
| `coordinator/` | TypeScript agent fleet runner |

The packfile layer is usable on its own:

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

## Not built yet

An S3 object backend and background repacking. Perry runs on a laptop or a single VM.

## Prior art

|  | Native git protocol | Cloneless read API | Agent fleets | Open source |
|---|:---:|:---:|:---:|:---:|
| [Pierre code.storage](https://code.storage) | Yes | Yes | Yes | No |
| [Entire](https://entire.io) | Uses yours | No | Client-side | Yes |
| [Gitea](https://gitea.io) | Via `git` | No | No | Yes |
| [Soft Serve](https://github.com/charmbracelet/soft-serve) | Via `git` | No | No | Yes |
| [go-git](https://github.com/go-git/go-git) | Library only | No | No | Yes |
| [Gitaly](https://gitlab.com/gitlab-org/gitaly) | Via `git` | Internal gRPC | No | Yes |
| Perry | From scratch | Yes | Yes | Yes |

Pierre's code.storage is the closest thing to Perry and a far more mature product.
Perry exists because there is no open implementation of the same idea.

[`gitformat-pack`](https://git-scm.com/docs/gitformat-pack) and
[`gitprotocol-http`](https://git-scm.com/docs/gitprotocol-http) are the real
specification. [go-git](https://github.com/go-git/go-git) is the mature pure-Go git
library; use it if you want a client.
[Gitaly](https://gitlab.com/gitlab-org/gitaly) is GitLab's git storage server and the
closest prior art on the server side.
