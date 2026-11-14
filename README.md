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

That is code access, not a finished task. With a real model in the loop Perry finished
20 of 20 agent tasks at a median 18.3 s against a shallow clone's 22.5 s, moving 2.4 KB
per task against 1.81 MB, and **cost 1.19 times as much in tokens**. If you pay for
inference and not for bandwidth or wall clock, a shallow clone is cheaper.
[Every run and every confound](BENCHMARKS.md).

## Install

```bash
git clone https://github.com/FloareDor/perry && cd perry
make build
./perry serve -data ./data
```

`go install` does not work yet: the module path in `go.mod` is
`github.com/perry-dev/perry`, which is not where the repository lives. Build from
source until that is reconciled.

The server listens on `127.0.0.1:8080`, keeps one SQLite file per repository under
`-data`, and needs no other process. Pushing to a name that does not exist creates it:

```bash
git remote add perry http://localhost:8080/app.git
git push perry main
```

[TUTORIAL.md](TUTORIAL.md) takes five minutes end to end: start a server, push a real
repository in, point Claude Code at it, and watch an agent land a commit from an empty
directory. Every command in it was executed and every transcript is real output.

## Point an agent at it

`perry mcp` bridges one repository to any agent that speaks the
[Model Context Protocol](https://modelcontextprotocol.io). It runs on stdio, so
connecting an agent you already use is a few lines of its config:

```bash
claude mcp add perry --env PERRY_TOKEN=... -- perry mcp -server http://perry.internal:8080 -repo app
```

Seven tools come with it: `perry_grep`, `perry_read_file`, `perry_list_files`,
`perry_list_tree`, `perry_list_branches`, `perry_create_branch`, `perry_commit`.
The agent searches, reads, edits and commits without a clone, a checkout, a staging
area or a push, and `git clone` of the branch it wrote returns a normal repository.

`perry_commit` takes search-and-replace edits, not a unified diff. Each `old` string
must occur exactly once or the commit is refused:

```json
{"changes": [{"path": "server.go", "edits": [
  {"old": "timeout := 30 * time.Second", "new": "timeout := 90 * time.Second"}
]}]}
```

A diff carries line numbers and context a model reconstructs from memory, and a wrong
one can still apply in the wrong place. An exact string either occurs once or it does
not, so the ambiguity is rejected at the server rather than guessed at.

Reading and writing over plain HTTP works the same way, with `expect` turning every
commit into a compare-and-swap. See **[API.md](API.md)** for the full surface, and
`coordinator/` for running a fleet of agents from TypeScript.

## Why Perry

**Most agent tasks touch a handful of files.** Perry serves grep and file-at-revision
against the object store, so an agent reads what it needs and skips the checkout.

**Concurrent pushes do not queue.** Packs are immutable, so a push writes a new one
rather than locking an existing one. The only serialization point is a
compare-and-swap on one ref row. Fifty agents each landed a commit on the same branch
in 393 ms, retrying 2.3 times each; fifty git agents took 2 m 26 s and retried 24.4
times each, because a losing writer re-runs a fetch, a reset and a push rather than
re-reading one row.

**Forks are free.** Branching a repository for a new agent writes one ref and copies
no object data: 171 µs, zero objects.

**It is still git.** `git clone`, `git push` and `git fetch` work against it with the
stock client, so everything downstream keeps working. Packs Perry writes pass
`git index-pack`; a repository pushed in and cloned back out is byte-identical and
`git fsck` clean.

## Why not Perry

**It costs more per task than a checkout does.** About 1.2 times the tokens across the
twenty tasks measured. Its tools take six turns to git's four and ride in a larger
prompt re-read on every one.

**It does not serve shallow or partial clones.** `--depth` is refused and `--filter`
is ignored, so a client that wants a checkout gets the whole history. An agent that
needs a real working tree, to run a build or a test suite, pays more against Perry
than against a host that supports both.

**Its authentication is one shared token.** One token covers every repository and
every route, sent in clear text over Basic auth unless you terminate TLS in front of
it. No users, no per-repository scopes, no revocation. Enough for a fleet of agents
inside one trust boundary, not enough for anything else.

**It is a storage and protocol layer and will stay one.** No web UI, pull requests, CI
or code review. Smart HTTP only: no SSH transport, no Git LFS, no GitHub mirroring.

**If one team of humans shares one repository, use anything else.** The design assumes
many short-lived branches written by machines.

## Running it on a network

Git smart HTTP carries `receive-pack`, so a Perry server without a token accepts
anonymous **pushes** to any repository and creates repositories on demand. Perry
refuses to bind anything but loopback unless you set a token or pass `-insecure`.

```bash
perry serve -addr 0.0.0.0:8080 -token "$PERRY_TOKEN"
```

The token is required on every route, git and API alike, as either an
`Authorization: Bearer` header or the password in HTTP Basic auth. Git cannot send a
bearer token, so it uses Basic and the username is ignored:

```bash
git clone http://x:"$PERRY_TOKEN"@host:8080/app.git
curl -H "Authorization: Bearer $PERRY_TOKEN" http://host:8080/v1/repos/app/refs
```

`/healthz` stays open so a liveness probe needs no credential. Put the server behind
TLS, because Basic auth sends the token in clear text.

## Status

**Alpha.** One author, one machine, two corpora. Run it on a laptop or a single VM; do
not put production data behind it yet.

The object model, packfile layer, wire protocol, smart HTTP server, SQLite store, HTTP
API, MCP adapter and TypeScript coordinator are all complete and cross-checked against
git 2.35. An S3 backend and background repacking are not started.

Benchmarks are one machine, AMD Ryzen 7 5800H, Windows 11, Go 1.26. The agent numbers
are a single model, `claude-sonnet-5-5`. Reproduce with `make corpora && make bench`.

Perry lost the first run of its own agent benchmark by three to four times, at 4.6
times the cost, and the [write-up](BENCHMARKS.md#a-real-agent-with-a-model-in-the-loop)
keeps that run and the two fixes that followed, one of which did nothing.

## Alternatives

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
Perry exists because there is no open implementation of the same idea. If you want a
git *client* library, use [go-git](https://github.com/go-git/go-git).

## Docs

| | |
|---|---|
| [TUTORIAL.md](TUTORIAL.md) | Five minutes, start to finish, with real transcripts |
| [API.md](API.md) | The HTTP API, the MCP tools, the fleet coordinator |
| [ARCHITECTURE.md](ARCHITECTURE.md) | How the storage, protocol and packing work |
| [BENCHMARKS.md](BENCHMARKS.md) | Every measurement, every baseline, every known limit |
| [TESTING.md](TESTING.md) | What is checked against the `git` binary, and how |

[`gitformat-pack`](https://git-scm.com/docs/gitformat-pack) and
[`gitprotocol-http`](https://git-scm.com/docs/gitprotocol-http) are the real
specification. [Gitaly](https://gitlab.com/gitlab-org/gitaly) is the closest prior art
on the server side.

There is also a [60-second launch video](https://github.com/FloareDor/perry/releases/download/v0.1.0/launch.mp4).
`video/` is the Remotion project that renders it.

## License

MIT. See [LICENSE](LICENSE).
