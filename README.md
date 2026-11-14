# Perry

[![CI](https://github.com/FloareDor/perry/actions/workflows/ci.yml/badge.svg)](https://github.com/FloareDor/perry/actions/workflows/ci.yml)
[![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-00ADD8)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Status: alpha](https://img.shields.io/badge/status-alpha-yellow)](#status)

**A git server your agents never have to clone.**

Perry serves clone, push and fetch over the git wire protocol, and it serves an HTTP
API that lets an agent grep a repository, read one file at one revision, fork a branch
and commit to it with no checkout anywhere.

Twenty agents starting work on a 500 MB repository pull 10 GB between them before one
of them reads a line. Finding one file in grpc-go, 6,100 commits and 1,424 files:

| | Time | Over the wire | Left on disk |
|---|---:|---:|---:|
| full clone, then `git grep` | 5.55 s | 44.44 MB | 60.67 MB |
| shallow clone, sparse, *told the directory* | 2.23 s | 746 KB | 3.08 MB |
| **grep the API, read the file** | **728 ms** | **27.2 KB** | **0 B** |

That measures code access, before a model has done anything with it. With a real
model in the loop Perry finished 20 of 20 agent tasks at a median 18.3 s against a
shallow clone's 22.5 s, moving 2.4 KB per task against 1.81 MB. It cost 1.19 times as
much in tokens. If you pay for inference and not for bandwidth or wall clock, a
shallow clone is cheaper.
[Every run and every confound](BENCHMARKS.md).

## Install

```bash
go install github.com/FloareDor/perry/cmd/perry@main
perry serve -data ./data
```

`@main` rather than `@latest`, because the v0.1.0 tag predates the module path this
repository now uses. One SQLite file per repository under `-data`, no other process.
Pushing to a name that does not exist creates it:

```bash
git remote add perry http://localhost:8080/app.git
git push perry main
```

Perry binds loopback and refuses a public address without a token.
[TUTORIAL.md](TUTORIAL.md) is five minutes end to end, with real transcripts.

## Point an agent at it

`perry mcp` bridges one repository to any agent that speaks the
[Model Context Protocol](https://modelcontextprotocol.io):

```bash
claude mcp add perry --env PERRY_TOKEN=... -- perry mcp -server http://perry.internal:8080 -repo app
```

Seven tools come with it: `perry_grep`, `perry_read_file`, `perry_list_files`,
`perry_list_tree`, `perry_list_branches`, `perry_create_branch`, `perry_commit`. The
agent searches, reads, edits and commits without a clone, a checkout, a staging area
or a push, and `git clone` of the branch it wrote returns a normal repository.

`perry_commit` takes search-and-replace edits, not a unified diff. Each `old` string
must occur exactly once or the commit is refused, so an edit that could land in two
places is rejected rather than guessed at. Every write carries `expect`, which makes
it a compare-and-swap. See [API.md](API.md).

## Why Perry

**Most agent tasks touch a handful of files.** Perry serves grep and file-at-revision
against the object store, so an agent reads what it needs and skips the checkout.

**Concurrent writes do not queue.** Fifty agents each landed a commit on the same
branch in 393 ms, retrying 2.3 times each. Fifty git agents took 2 m 26 s and retried
24.4 times each, because a losing writer re-runs a fetch, a reset and a push rather
than re-reading one ref row. Forking a branch is 171 µs and copies no objects.

**It is still git.** Packs Perry writes pass `git index-pack`; a repository pushed in
and cloned back out is byte-identical and `git fsck` clean.

## Why not Perry

**It costs more per task than a checkout does**, about 1.2 times the tokens across the
twenty tasks measured. Its tools take six turns to git's four, in a larger prompt
re-read on every one.

**It does not serve shallow or partial clones.** An agent that needs a real working
tree, to run a build or a test suite, pays more against Perry than against a host that
supports both.

**Its authentication is one shared token** for every repository and every route, with
no users, no scopes and no revocation. Enough for a fleet of agents inside one trust
boundary, not enough for anything else.

**It is a storage and protocol layer and will stay one.** No web UI, pull requests, CI
or code review. If one team of humans shares one repository, use anything else.

## Status

**Alpha.** One author, one machine, two corpora. Run it on a laptop or a single VM; do
not put production data behind it yet. An S3 backend and background repacking are not
started; everything else is complete and cross-checked against git 2.35.

Benchmarks are one machine, AMD Ryzen 7 5800H, Windows 11, Go 1.26, and the agent
numbers are a single model. Perry lost the first run of its own agent benchmark by
three to four times, and the
[write-up](BENCHMARKS.md#a-real-agent-with-a-model-in-the-loop) keeps that run and the
two fixes that followed, one of which did nothing.

[Pierre code.storage](https://code.storage) is the closest thing to Perry and a far
more mature product, but it is not open. Perry exists because there is no open
implementation of the same idea. For a git *client* library, use
[go-git](https://github.com/go-git/go-git).

## Docs

| | |
|---|---|
| [TUTORIAL.md](TUTORIAL.md) | Five minutes, start to finish, with real transcripts |
| [API.md](API.md) | The HTTP API, the MCP tools, auth, the fleet coordinator |
| [ARCHITECTURE.md](ARCHITECTURE.md) | How the storage, protocol and packing work |
| [BENCHMARKS.md](BENCHMARKS.md) | Every measurement, every baseline, every known limit |
| [TESTING.md](TESTING.md) | What is checked against the `git` binary, and how |

There is also a [60-second launch video](https://github.com/FloareDor/perry/releases/download/v0.1.0/launch.mp4).

MIT. See [LICENSE](LICENSE).
