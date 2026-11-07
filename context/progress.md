# Perry: build progress

Build log. Plan: [plan.md](plan.md). Newest entry first.

## Status

| Week | Milestone | State |
|---|---|---|
| 1 | Objects + pack reader | **done** |
| 1 | Pack writer with delta compression | **done** |
| 1 | Pack v2 index | **done** |
| 2 | pkt-line + upload-pack, `git clone` works | **done** |
| 3 | receive-pack, `git push` works | **done** |
| 3 | Compare-and-swap refs, atomic batches | **done** |
| 4 | Concurrency: 20 agents, distinct branches | **done** |
| 3 | SQLite store backend | not started |
| 4 | S3 store backend | not started |
| 5 | Cloneless read API: grep, file-at-ref, fork | not started |
| 5 | Benchmarks vs `git http-backend` | not started |
| 6 | Agent fleet coordinator | not started |
| 6 | Remotion launch video | not started |
| - | README with real benchmarks | **done** |

## Verified against real git 2.35

Agreeing with our own code proves nothing. Everything here is checked against
the git binary.

| Check | Result |
|---|---|
| `git hash-object` vs our hashing, four payload shapes to 800 KB | Match |
| `git hash-object -t tree` vs our tree encoder and sort order | Match |
| Read a pack git wrote: 1078 objects, 745 deltas | Every hash verified |
| `git cat-file -t` vs our decoded object type | Match |
| `git index-pack` on a pack we wrote | Accepted |
| Our pack index vs `git verify-pack -v`, 1078 objects | All names and offsets match |
| `git verify-pack` on an index we wrote | Accepted |
| Push then clone, 8 commits | History identical, `git fsck --strict` clean |
| Incremental fetch after a second push | Only new objects transferred |
| Non-fast-forward push | Refused by compare-and-swap |
| Five branches pushed with `--all` | All present in the clone |
| Branch delete, which sends no packfile | Ref removed |
| Clone of an empty repository | Fails cleanly, no hang, no corrupt repo |
| **20 agents pushing distinct branches at once** | **20/20 in 709 ms** |

## Log

### 2026-11-07: the server works

A real `git` client can now clone from and push to perry.

Added the v2 pack index, `internal/protocol` (pkt-line framing, reachability
walk, upload-pack, receive-pack), `internal/store` (interface plus an
in-memory backend), and `internal/server` (smart HTTP routing).

The concurrency result is the headline: twenty agents pushing twenty distinct
branches simultaneously all succeeded, in 709 ms total. Each push touches its
own reference row, so there is nothing for them to contend over.

**Design points that earned their place:**

- Objects are written before any reference moves. An interrupted push leaves
  unreferenced objects, which a GC reclaims, rather than references pointing
  at objects that were never stored.
- Reference updates go through one atomic batch. A conflict reports which
  reference raced and marks the others "not attempted", so a client never
  sees a half-applied push.
- upload-pack refuses a want that no advertised reference reaches. Without
  that check, a client that learned an object name out of band could read a
  force-pushed branch that had not been collected yet.
- The reachability walk marks the client's haves in a separate pass before
  collecting. One combined pass would send or skip a shared object depending
  on which side the traversal happened to reach first.
- `Reader.Rest()` stitches the pkt-line buffer back in front of the
  underlying reader. A push sends references as packets and then the packfile
  as raw bytes on the same stream, so the buffered read-ahead already holds
  the first chunk of the pack.

**Next:** SQLite backend for persistence, then the cloneless read API.

### 2026-11-07: benchmarks, a 108x fix, and the README

Added `internal/bench`, measured against a real repository, wrote the README.

Corpus: shallow bare clone of spf13/cobra. 1078 objects, 10.19 MB
uncompressed, 69% of pack entries are deltas. AMD Ryzen 7 5800H, Go 1.21.

**A 108x read regression, caused by the correctness fix above.**
Making the counting reader implement `io.ByteReader` was right: it stops
`compress/flate` wrapping the input in a `bufio.Reader` and reading ahead,
which is what made entry offsets wrong. It also meant flate pulled one byte
at a time, and each byte became a `ReadAt` against the file. Resolving the
corpus took 4.32 s at 124 KB/s. Fixed by reading each entry into memory with
a single `ReadAt` and inflating from a `bytes.Reader`, which is already a
`ByteReader`. The slab size is known in advance because deflate's worst case
is bounded at five bytes per 65535 bytes of input. Now 40 ms at 13.16 MB/s.

Also removed a double inflate: `BuildOffsetIndex` scanned every entry and
then re-read each one. `Scan` now hands the inflated bytes to its callback.

**Compression is 4.2x worse than git's at the default window, and the cause
is object ordering, not the delta search.**

| Window | Pack size | vs raw | vs git | Pack time |
|---:|---:|---:|---:|---:|
| 0 | 2.63 MB | 3.9x | 5.02x | 0.46 s |
| 10 | 2.20 MB | 4.6x | 4.19x | 2.00 s |
| 25 | 1.69 MB | 6.0x | 3.21x | 4.25 s |
| 50 | 1.22 MB | 8.4x | 2.32x | 8.26 s |
| 100 | 825 KB | 12.7x | 1.54x | 16.00 s |
| 250 | 535 KB | 19.5x | 1.00x | 37.50 s |

At window 250 we match git's 537 KB exactly, in 37 seconds against git's well
under one. Git sorts objects by type, then filename, then descending size, so
revisions of one file are adjacent and the right base is always a few slots
away. Perry sorts by type and size only because the packing layer has no path
information, so it compensates by searching wider.

**Fix, queued after the server:** thread paths down from the tree walk and
sort on them. Expect window 10 to reach roughly window 250 compression.

Delta codec throughput: `ApplyDelta` 6.36 GB/s, `MakeDelta` 159 MB/s.

### 2026-11-07: object model and packfiles

Built and verified the two lowest layers.

`internal/object` covers the four object types, SHA-1 content addressing,
loose zlib encoding, and tree, commit and tag parsing. Commit re-encoding is
byte stable, including folded `gpgsig` headers and timezone offsets, which is
load bearing: re-encoding a parsed commit differently would change its hash
and break history. Headers perry does not model pass through verbatim in
their original order.

`internal/pack` covers the delta codec, a reader for both delta forms, and a
writer with a delta search. The codec round trips under a 300-case fuzz with
random insert, delete and overwrite mutations. The reader bounds chain depth
at 100 against cyclic packs, caches resolved objects by offset, and is safe
for concurrent readers. The writer deltifies against a 10-object window with
a depth limit of 50, accepting a delta only when it is at least 10% smaller
than the object.

**Three real bugs, all found by tests rather than by reading:**

1. *Delta matcher collapsed on repetitive input.* The base index kept one
   offset per block hash, so on content with a repeating line every hash
   resolved to its last occurrence near the end of the base. A copy that
   should have spanned the whole file stopped immediately and the delta
   fragmented into thousands of short copies: 2522 bytes where tens would do.
   Fixed by keeping up to four candidate offsets per hash and extending each,
   taking the longest match.

2. *Pack entry byte count overshot.* `compress/flate` wraps a plain reader in
   a `bufio.Reader` and reads ahead, so the count of consumed bytes ran past
   the end of each deflate stream. Pack entries are stored back to back with
   no separator, so the scanner placed every entry after the first at the
   wrong offset.

3. *Offset deltas pointed a few bytes short of their base.* The writer
   measured the backward distance from the position after the entry header
   rather than from the header's start, while the reader measured from the
   start. Every delta in every pack was wrong by the header's length.

One test expectation was also wrong rather than the code: git sorts a tree
entry for a directory as though its name ended in `/`, and since `.` (0x2E)
is below `/` (0x2F) which is below `0` (0x30), the entries `a.c`, `a` (a
directory) and `a0` sort in that order.
