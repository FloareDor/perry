<div align="center">

# Perry

**A git server built for machines.**

Native git protocol, pluggable object storage, and a read API that lets a coding agent
grep a repository without cloning it.

[![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Status](https://img.shields.io/badge/status-early%20development-orange)](#project-status)
[![Tests](https://img.shields.io/badge/cross--checked%20against-git%202.35-f05032?logo=git&logoColor=white)](#how-it-is-tested)

</div>

---

## The problem

A coding agent starts the same way every time: it clones the repository. All of it.

That was fine when a repository had a handful of humans attached to it. It is not fine
when twenty agents are working the same backlog in parallel. Twenty clones of a 500 MB
repository is 10 GB of transfer to do nothing at all, and the agents have not yet read a
single line of code. Push them back and git serializes every one of those branches on a
single repository lock.

Agents do not need a working copy. They need to find three files and read them. Perry is
the server that gives them that, while still speaking git to everything else.

```
┌─ git clone / push / fetch ──────┐
│   (smart HTTP, real git)        ├──▶  Perry
└─────────────────────────────────┘      │
┌─ GET /v1/repos/x/grep ──────────┐      ├─ objects ─▶ SQLite  (laptop)
│  GET /v1/repos/x/file?ref=..    ├──▶   │            S3      (production)
│  POST /v1/repos/x/fork          │      └─ refs ────▶ compare-and-swap, one row per ref
└─────────────────────────────────┘
```

> [!WARNING]
> Perry is under active construction. The object and packfile layers are finished and
> verified against the real git binary. The server is not written yet. See
> [Project status](#project-status) before you plan anything around it.

## Project status

| Layer | What it does | State |
|---|---|---|
| `internal/object` | Four object types, SHA-1 addressing, byte-stable re-encoding | **Done** |
| `internal/pack` | Packfile reader, both delta forms, writer with delta search | **Done** |
| `internal/pack` | v2 pack index (`.idx`) | In progress |
| `internal/protocol` | pkt-line, `upload-pack` so `git clone` works | Not started |
| `internal/protocol` | `receive-pack` so `git push` works | Not started |
| `internal/store` | SQLite and S3 backends | Not started |
| `internal/refs` | Compare-and-swap ref updates under concurrent pushes | Not started |
| `internal/api` | Cloneless grep, file-at-ref, zero-copy fork | Not started |

Explicitly out of scope, and staying that way: a web UI, pull requests, CI, SSH
transport, Git LFS, and GitHub mirroring. Perry is a storage and protocol layer. The
forge is somebody else's product.

## Quickstart

```bash
git clone https://github.com/perry-dev/perry
cd perry
go test ./...
```

To run the benchmarks you need a real repository to measure against:

```bash
git clone --bare --depth 200 https://github.com/spf13/cobra testdata/cobra.git
go test ./internal/bench -v
```

The library is usable today for reading and writing packfiles:

```go
// Read every object out of a packfile, deltas resolved.
f, _ := os.Open("objects/pack/pack-abc123.pack")
st, _ := f.Stat()

r, _ := pack.NewReader(f, st.Size())
byID, _ := r.BuildOffsetIndex()

for id, offset := range byID {
    obj, _ := r.ObjectAt(offset)
    fmt.Printf("%s  %-6s  %d bytes\n", id.Short(8), obj.Type, obj.Size())
}
```

```go
// Write a packfile git will accept.
var buf bytes.Buffer
name, _ := pack.WriteAll(&buf, objects)
fmt.Println("pack name:", name)
```

## Benchmarks

Measured on an AMD Ryzen 7 5800H, Windows 11, Go 1.21. The corpus is a shallow bare
clone of [spf13/cobra](https://github.com/spf13/cobra): **1078 objects, 10.19 MB
uncompressed, 69% of entries stored as deltas**. Reproduce with
`go test ./internal/bench -v`.

### Reading a real packfile

Resolving every object in git's own packfile, delta chains included.

| | Throughput | Time for the corpus |
|---|---|---|
| Before the slab fix | 124 KB/s | 4.32 s |
| **After** | **13.16 MB/s** | **40 ms** |

```
Pack resolution, 537 KB corpus
before  ████████████████████████████████████████████████████  4320 ms
after   ▌                                                       40 ms
```

That 108x is one bug, described in [What I got wrong](#what-i-got-wrong).

### Delta codec

| Operation | Throughput |
|---|---|
| `ApplyDelta` | 6.36 GB/s |
| `MakeDelta` | 159 MB/s |

Applying is two orders of magnitude faster than searching, which is the right shape:
a delta is written once and read many times.

### Compression, and the trade it exposes

Perry's packer against git's own output for the same 1078 objects. The delta window is
how many recently written objects each new object is compared against.

| Window | Pack size | vs. uncompressed | vs. git | Pack time |
|---:|---:|---:|---:|---:|
| 0 (no deltas) | 2.63 MB | 3.9x | 5.02x | 0.46 s |
| 10 (default) | 2.20 MB | 4.6x | 4.19x | 2.00 s |
| 25 | 1.69 MB | 6.0x | 3.21x | 4.25 s |
| 50 | 1.22 MB | 8.4x | 2.32x | 8.26 s |
| 100 | 825 KB | 12.7x | 1.54x | 16.00 s |
| **250** | **535 KB** | **19.5x** | **1.00x** | 37.50 s |

```
Pack size by delta window        (git's own pack = 537 KB)
   0  ████████████████████████████████████████████████  2.63 MB
  10  ████████████████████████████████████████          2.20 MB
  25  ██████████████████████████████                    1.69 MB
  50  ██████████████████████                            1.22 MB
 100  ███████████████                                   825 KB
 250  █████████                                         535 KB  ◀ matches git
      └─────────────────────────────────────────────────
```

> [!NOTE]
> At a window of 250 Perry's pack is 535 KB against git's 537 KB, so compression is a
> dead heat. It takes 37 seconds to get there, and git does it in well under one.

That gap is not a mystery, and it is not really about the delta search. Git sorts objects
by type, then by filename, then by descending size before packing, so successive
revisions of one file land next to each other and the right base is always a few slots
away. Perry currently sorts by type and size only, because at the packing layer it has no
path information. So it compensates by looking further, and pays for the search.

The fix is to thread paths down from the tree walk and sort on them, which should put
Perry's default window back near 10 with window-250 compression. That is the next
substantial piece of work after the server.

## Why I built this

I kept twenty terminal tabs open, each with an agent working a different ticket on the
same repository, and I kept watching all twenty of them spend their first fifteen seconds
doing exactly the same clone. Then I watched them queue behind each other to push. The
work the agents were doing was new. Everything around it was infrastructure written for a
world where a repository had six people attached to it and they went home at night.

I also wanted to actually understand git, rather than understand the forty commands on
top of it. There is a particular kind of knowledge you only get by writing the packfile
format yourself and having `git index-pack` tell you, with no detail whatsoever, that
your pack is wrong.

So the rule for this project is that nothing counts until the real git binary agrees. Not
a reimplementation that passes its own tests. A pack that `git index-pack` accepts, and a
pack git wrote that Perry reads back with every hash matching.

## What I got wrong

Three bugs that tests caught and reading did not. I am writing them down because each one
was invisible in the code and obvious in the output.

**The delta matcher collapsed on repetitive files.** I indexed the base in 16-byte blocks
and kept one offset per block hash. On a file that is the same line repeated, every hash
resolves to its *last* occurrence, near the end of the base. So a copy that should have
spanned the entire file hit the end immediately, and the delta shattered into thousands of
tiny copies: 2522 bytes to describe an edit that needed about twenty. Keeping four
candidate offsets per hash and extending each one, then taking the longest match, fixed it.

**The pack scanner put every entry after the first in the wrong place.** Pack entries are
stored back to back with no length prefix and no separator, so the only way to find entry
*k+1* is to decompress entry *k* and see exactly where its stream ended. `compress/flate`
wraps a plain `io.Reader` in a `bufio.Reader` and reads ahead, so my count of consumed
bytes overshot the end of every stream. I fixed it by implementing `io.ByteReader`, which
makes flate read through my counter directly.

**That fix then made reading 108x slower.** Satisfying the `io.ByteReader` interface meant
flate was now pulling one byte at a time, and each byte became a `ReadAt` against the
file. Correct, and 124 KB/s. The real answer is to read the whole entry into memory in a
single `ReadAt` and inflate from a `bytes.Reader`, which is already a `ByteReader` and
costs nothing per byte. You can size that read in advance because deflate's worst case is
bounded: five bytes per 65535 bytes of input. Same exact count, one syscall, 13.16 MB/s.

**Offset deltas all pointed a few bytes short of their base.** An offset delta stores the
backward distance to its base. I wrote that distance measured from *after* the entry
header, and read it measured from the *start* of the entry header. Every delta in every
pack was wrong by the length of one header, which is a number that changes with the
object's size, so the corruption looked random.

One more, for honesty: a test failed and the test was wrong, not the code. Git sorts a
tree entry for a directory as if its name ended in `/`. Since `.` is 0x2E, `/` is 0x2F and
`0` is 0x30, the entries `a.c`, `a` (a directory) and `a0` sort in that order, with the
directory in the middle. I had confidently expected it last.

## How it is tested

Agreeing with yourself proves nothing, so every layer is checked against git 2.35.

| Check | Result |
|---|---|
| `git hash-object` vs. our hashing, four payload shapes to 800 KB | Match |
| `git hash-object -t tree` vs. our tree encoder and its sort order | Match |
| Read a pack git wrote: 1078 objects, 745 deltas | Every hash verified |
| `git cat-file -t` vs. our decoded object type | Match |
| `git index-pack` on a pack we wrote | Accepted |
| Delta round trip under 300 random insert/delete/overwrite mutations | Pass |

Corruption is tested as deliberately as correctness: bad magic, wrong version, truncated
streams, flipped bytes in the body and in the trailer, reserved delta opcodes, copies that
run past the end of the base, and declared sizes that disagree with the payload.

## Design notes

**Packs are immutable.** A push writes a new pack rather than rewriting an existing one,
and repacking happens in the background. Nothing has to take a write lock on stored
objects, so concurrent pushes never contend over them.

**Refs are the only serialization point.** Every ref update is a compare-and-swap against
one row. Twenty agents pushing twenty branches touch twenty different rows and never meet.

**A fork is a ref write.** Branching a repository for a new agent copies no object data at
all.

**Delta chains are bounded at depth 100 on read.** A corrupt or hostile pack can describe
a cycle, and without the bound resolving it would recurse until the process died.

## Prior art

Perry is not the first to notice this. It is trying to be the open one.

| | Native git protocol | Cloneless read API | Built for agent fleets | Open source |
|---|:---:|:---:|:---:|:---:|
| [Pierre code.storage](https://code.storage) | Yes | Yes | Yes | No |
| [Entire CLI](https://entire.io) | Uses yours | No | Yes, client-side | Yes |
| [Gitea](https://gitea.io) / [Soft-Serve](https://github.com/charmbracelet/soft-serve) | Via the git binary | No | No | Yes |
| [go-git](https://github.com/go-git/go-git) | Library only | No | No | Yes |
| [Gitaly](https://gitlab.com/gitlab-org/gitaly) | Via the git binary | Internal gRPC | No | Yes |
| **Perry** | Yes, from scratch | Yes | Yes | Yes |

Pierre's `code.storage` is the closest thing and clearly the more mature product. Perry
exists because there is no open implementation of the same idea, and because I wanted to
know how it works.

## Acknowledgements

The git source tree's own format documentation is the real specification, in particular
`gitformat-pack.txt`, `gitprotocol-pack.txt` and `gitprotocol-http.txt`. `go-git`'s
packfile reader was invaluable for checking my delta resolution against something that
already worked.
