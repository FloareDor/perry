# Perry: build progress

Living log for the autonomous build loop. Plan: [plan.md](plan.md).
Newest entry at the top of the log.

## Status

| Week | Milestone | State |
|---|---|---|
| 1 | Objects + pack reader | **done** |
| 1 | Pack writer with delta compression | **done** |
| 1 | Pack v2 index (.idx) | not started |
| 2 | pkt-line + upload-pack (clone) | not started |
| 3 | receive-pack (push) + SQLite store + CAS refs | not started |
| 4 | S3 backend + 40-way concurrency | not started |
| 5 | Cloneless read API + benchmarks | not started |
| 6 | Agent fleet demo + Remotion video + writeup | not started |

## Verified against real git

These are the checks that matter. Agreeing with our own code proves nothing.

| Check | Result |
|---|---|
| `git hash-object` vs `object.Hash`, 4 payload shapes incl. 800 KB | match |
| `git hash-object -t tree` vs our tree encoder | match |
| Read a pack git wrote: 90 objects, 14 deltas, every hash verified | pass |
| `git cat-file -t` agrees with our decoded type, 40 sampled objects | pass |
| `git index-pack` on a pack we wrote: 90 objects, 5130 bytes | accepted |

Our 90-object pack came out at 5130 bytes against git's 5548 for the same
objects. The comparison is not apples to apples, since `git gc` chooses its
own object set and ordering, but it does rule out the writer being wildly
inefficient.

## Log

### 2026-11-07 — iteration 1: object model and packfiles

Built and verified the two lowest layers.

**`internal/object`** — the four object types, SHA-1 content addressing, loose
zlib encoding, tree and commit and tag parsing.

- Commit re-encoding is byte stable, including folded `gpgsig` headers and
  timezone offsets. This is load bearing: re-encoding a parsed commit
  differently would change its hash and break history.
- Unmodelled headers pass through verbatim in their original order rather
  than being dropped.

**`internal/pack`** — delta codec, pack reader with both delta forms, pack
writer with a delta search.

- `ApplyDelta` and `MakeDelta` round trip under a 300-case fuzz with random
  insert, delete and overwrite mutations.
- The reader resolves offset deltas and reference deltas, bounds chain depth
  at 100 against cyclic packs, caches resolved objects by offset, and is safe
  for concurrent readers.
- The writer deltifies against a 10-object window with a depth limit of 50,
  and only accepts a delta that is at least 10% smaller than the object.

**Three real bugs found and fixed, all by tests rather than by reading:**

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
   wrong offset. Fixed by having the counting reader implement
   `io.ByteReader`, which makes flate read through it directly.

3. *Offset deltas pointed a few bytes short of their base.* The writer
   measured the backward distance from the position after the entry header
   rather than from the header's start, while the reader measured from the
   start. Every delta in every pack was wrong by the header's length.

One test expectation was also wrong rather than the code: git sorts a tree
entry for a directory as though its name ended in `/`, and since `.` (0x2E)
is below `/` (0x2F) which is below `0` (0x30), the entries `a.c`, `a` (a
directory) and `a0` sort in that order. I had expected the directory last.

**Next:** the v2 pack index, then pkt-line framing and `upload-pack` so a
real `git clone` works against the server.
