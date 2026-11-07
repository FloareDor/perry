# Perry

[![Go Reference](https://img.shields.io/badge/go-reference-00ADD8?logo=go&logoColor=white)](https://pkg.go.dev/github.com/perry-dev/perry)
[![Go 1.21+](https://img.shields.io/badge/go-1.21%2B-00ADD8)](https://go.dev)
[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-orange)](#project-status)

Perry is a git server for coding agents. It serves clone, push and fetch over the git
wire protocol, and it serves a read API that lets an agent grep a repository or read one
file at one ref without cloning anything.

The protocol is implemented in Go rather than shelling out to the `git` binary. Objects
go to SQLite on a laptop or S3 in production.

## Project status

Pre-alpha. The object and packfile layers are complete and cross-checked against git
2.35. The server is not written yet.

| Package | Scope | Status |
|---|---|---|
| `internal/object` | Object model, SHA-1 addressing, loose encoding | Complete |
| `internal/pack` | Packfile reader, delta codec, pack writer | Complete |
| `internal/pack` | v2 pack index | In progress |
| `internal/protocol` | pkt-line, `upload-pack`, `receive-pack` | Not started |
| `internal/store` | SQLite and S3 backends | Not started |
| `internal/refs` | Compare-and-swap ref updates | Not started |
| `internal/api` | grep, file-at-ref, fork | Not started |

The library is usable today for reading and writing packfiles. Nothing else is.

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

**It does not work yet.** See [Project status](#project-status).

**It is a storage and protocol layer, and intends to stay one.** No web UI, pull
requests, CI or code review. Smart HTTP only: no SSH transport, no Git LFS, no GitHub
mirroring.

**Packs are larger than git's at the default settings.** Roughly 4x, for reasons covered
under [Benchmarks](#benchmarks). Closing that gap is planned work, not a solved problem.

**If one team of humans shares one repository, use anything else.** The design assumes
many short-lived branches written by machines.

## Installation

```bash
go get github.com/perry-dev/perry
```

Or build from source:

```bash
git clone https://github.com/perry-dev/perry
cd perry
go test ./...
```

## Examples

Read every object out of a packfile, delta chains resolved:

```go
f, _ := os.Open("objects/pack/pack-abc123.pack")
st, _ := f.Stat()

r, _ := pack.NewReader(f, st.Size())
byID, _ := r.BuildOffsetIndex()

for id, offset := range byID {
    obj, _ := r.ObjectAt(offset)
    fmt.Printf("%s  %-6s  %d bytes\n", id.Short(8), obj.Type, obj.Size())
}
```

Write a packfile the git client will accept:

```go
var buf bytes.Buffer
name, err := pack.WriteAll(&buf, objects)
```

Walk a tree:

```go
obj, _ := r.ObjectAt(offset)
tree, _ := object.DecodeTree(obj.Payload)

for _, e := range tree.Entries {
    fmt.Printf("%s %s %s\n", e.Mode, e.ID.Short(8), e.Name)
}
```

Trade compression against packing time:

```go
pw, _ := pack.NewWriter(w, uint32(len(objects)))
pw.SetWindow(100) // default is 10
pw.SetDepth(50)
```

## Benchmarks

AMD Ryzen 7 5800H, Windows 11, Go 1.21. The corpus is a shallow bare clone of
[spf13/cobra](https://github.com/spf13/cobra): 1078 objects, 10.19 MB uncompressed, 69%
of pack entries stored as deltas. Reproduce with:

```bash
git clone --bare --depth 200 https://github.com/spf13/cobra testdata/cobra.git
go test ./internal/bench -v
```

One corpus on one machine is never enough.

### Throughput

| Operation | Throughput |
|---|---|
| Resolve a packfile, deltas included | 13.2 MB/s |
| Apply a delta | 6.36 GB/s |
| Search for a delta | 159 MB/s |

Applying is two orders of magnitude faster than searching. A delta is written once and
read many times.

### Compression

Perry's packer against git's own output for the same 1078 objects. The delta window is
how many recently written objects each new object is compared against.

| Window | Pack size | vs. uncompressed | vs. git | Pack time |
|---:|---:|---:|---:|---:|
| 0 (off) | 2.63 MB | 3.9x | 5.02x | 0.46 s |
| **10** (default) | **2.20 MB** | **4.6x** | **4.19x** | **2.00 s** |
| 25 | 1.69 MB | 6.0x | 3.21x | 4.25 s |
| 50 | 1.22 MB | 8.4x | 2.32x | 8.26 s |
| 100 | 825 KB | 12.7x | 1.54x | 16.00 s |
| 250 | 535 KB | 19.5x | 1.00x | 37.50 s |

```
Pack size by delta window                     git's own pack: 537 KB
  0  ████████████████████████████████████████████████  2.63 MB
 10  ████████████████████████████████████████          2.20 MB
 25  ██████████████████████████████                    1.69 MB
 50  ██████████████████████                            1.22 MB
100  ███████████████                                    825 KB
250  █████████                                          535 KB
     └────────────────────────────────────────────────
```

At a window of 250 Perry matches git's compression exactly, and takes 37 seconds to do
what git does in under one.

The gap comes from object ordering. Git sorts objects by type, then filename, then
descending size, so successive revisions of a file land next to each other and a good
base is always within a few slots. Perry's packer has no path information and
sorts by type and size alone, so it compensates by searching wider. Threading paths down
from the tree walk should bring the default window back to 10 at window-250 compression.

## How it works

```
git clone / push / fetch ────┐
   smart HTTP, stock client  ├──▶  Perry
                             │      │
GET  /v1/repos/{r}/grep ─────┤      ├── objects ──▶  SQLite  (local)
GET  /v1/repos/{r}/file ─────┤      │                S3      (production)
POST /v1/repos/{r}/fork ─────┘      └── refs ─────▶  one row per ref, compare-and-swap
```

Packs are immutable. A push writes a new pack and repacking happens in the background,
so nothing takes a write lock on stored objects. That leaves refs as the only
serialization point, and every ref update is a compare-and-swap against one row, so
twenty branches touch twenty rows.

Reading is where the format gets awkward. Pack entries sit back to back with no
separator, so finding entry k+1 means decompressing entry k and knowing exactly where
its stream ended. Deflate's worst case is bounded at five bytes per 65535 bytes of
input, so Perry sizes the read in advance and each entry costs a single `ReadAt`.

Delta chains are bounded at depth 100 on read. A corrupt or hostile pack can describe a
cycle, and an unbounded resolver would recurse until the process died.

## Testing

```bash
go test ./...
```

Every layer is checked against the git binary, because a reimplementation that agrees
only with itself proves nothing.

| Check | Result |
|---|---|
| `git hash-object` vs. our hashing, four payload shapes up to 800 KB | Match |
| `git hash-object -t tree` vs. our tree encoder and sort order | Match |
| Read a pack git wrote: 1078 objects, 745 deltas | Every hash verified |
| `git cat-file -t` vs. our decoded object type | Match |
| `git index-pack` on a pack we wrote | Accepted |
| Delta round trip, 300 random insert/delete/overwrite mutations | Pass |

Corruption gets the same attention as correctness: bad magic, wrong version, truncated
streams, flipped bytes in the body and in the trailer, reserved delta opcodes, copies
running past the end of a base, and declared sizes that disagree with the payload.

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
