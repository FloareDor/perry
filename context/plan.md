# Perry: a git-compatible object store for coding-agent fleets

Six-week plan. Go server, SQLite/S3 storage, native git protocol, cloneless read API,
twenty-agent demo, and a YC-launch-style Remotion video with synced voiceover.

Working name: **perry** (binary `perry`, module `github.com/<you>/perry`).

---

## 1. Positioning: what exists, and the gap

| Tool | What it is | Git wire protocol | Cloneless read API (grep / file-at-ref) | Agent-fleet focus | Open source |
|---|---|---|---|---|---|
| **Pierre code.storage** | Hosted git-as-an-API for AI platforms (Lovable, Bolt, Amp) | Yes, custom domain | Yes: POST `/api/repos/{repo}/grep`, stream file, archive, blame; 4 MiB chunked streaming; ephemeral branches | Yes, core thesis | No (SDKs only) |
| **Entire CLI** (ex-GitHub CEO, $60M) | Records agent sessions as checkpoints on an `entire/checkpoints/v1` branch; rewind | Uses your existing git | No | Yes, but client-side | Yes |
| **go-git** | Pure-Go git library | Client + partial server plumbing | No | No | Yes |
| **Soft-Serve** (Charm) | Self-hostable git server with TUI, smart HTTP added in #291 | Yes (wraps git binary) | No | No | Yes |
| **Gitea / Gogs** | Full forge | Yes (shells out to git) | Web UI only | No | Yes |
| **Gitaly** | GitLab's internal git RPC server | Yes (wraps git) | Yes (gRPC, internal) | No | Yes, but not standalone |
| **Vercel Sandbox / Islo** | Sandboxes for agents, fs snapshots | n/a | n/a | Yes | CLI/SDK only |

**The gap perry fills:** an open-source server that (a) speaks native git without shelling out
to the git binary, (b) stores objects in a pluggable backend (SQLite locally, S3 in prod),
(c) gives agents a cloneless read API like code.storage, and (d) is built and benchmarked
for hundreds of short-lived concurrent agent branches. Nothing open does all four.

**Explicitly NOT building:** a web UI, PRs, CI, SSH transport, LFS, GitHub sync. Say so in the README.

---

## 2. Architecture

```
git clone/push (smart HTTP v1) ─┐
                                 ├─> perry server (Go, net/http)
agents via REST (grep, file@ref) ┘        │
                                          ├─ protocol/   pkt-line, upload-pack, receive-pack
                                          ├─ object/     blob/tree/commit/tag, SHA-1, zlib
                                          ├─ pack/       reader (OFS/REF delta), writer (window delta)
                                          ├─ refs/       CAS ref store + transaction log
                                          ├─ store/      interface { Get, Put, Has, Walk }
                                          │    ├─ sqlite/   local, single file
                                          │    └─ s3/       immutable packs + local LRU cache
                                          └─ api/        /v1/repos/{r}/grep, /file, /tree, /fork, webhooks
```

Key decisions
- **Protocol v1 only.** v2 is optional for clone/push and doubles the surface.
- **Packs are immutable.** New pushes write a new pack; a background job repacks. Avoids locking.
- **Refs use compare-and-swap** backed by a single SQLite table (even on the S3 backend) so concurrent
  pushes serialize only on the ref row, never on objects.
- **Grep walks trees from the store, not a checkout.** Stream results; stop on `limit`.
- **Fork = ref copy.** `/fork` creates `refs/heads/agent-<id>` pointing at the same commit. Zero bytes copied.

---

## 3. Six-week schedule

Every week ends with a demo-able artifact and a test that real `git` passes against the server.

### Week 1: Objects + pack reader
- Blob/tree/commit/tag encode/decode, SHA-1, zlib loose objects.
- Pack + idx parser with OFS and REF delta resolution.
- CLI: `perry cat-file`, `perry verify-pack`.
- **Done:** unpack every pack in the `gin` and `cobra` repos; every hash matches `git cat-file`.

### Week 2: Clone works
- pkt-line framing, ref advertisement, `want`/`have` negotiation, pack writer with
  sliding-window delta (window 10, depth 50, no fancy heuristics).
- `GET /info/refs?service=git-upload-pack`, `POST /git-upload-pack`.
- **Done:** `git clone http://localhost:8080/demo.git` with real git, `git fsck` clean.

### Week 3: Push works + storage interface
- `POST /git-receive-pack`: receive pack, index, CAS ref update, report-status.
- `store.Store` interface, SQLite backend. Ref transaction log.
- **Done:** push, force-push, delete-branch; stale-SHA push rejected; fetch of new refs works.

### Week 4: S3 backend + concurrency
- S3 (MinIO locally) backend: packs as immutable objects, local disk LRU cache, idx cached in SQLite.
- Load tool `perry bench push --agents 40`: distinct branches + contended same-branch.
- Fix races. Run `perry fsck` after each run.
- **Done:** 40 concurrent pushes, zero corruption, zero lost ref updates, 20 repeated runs. p50/p99 recorded.

### Week 5: Cloneless read API + webhooks + benchmarks
- `/v1/repos/{r}/file?ref=&path=` (stream), `/tree`, `/grep` (regex, path filters, context, limit, stream),
  `/fork`, per-repo bearer tokens, `push` webhook.
- Benchmarks vs bare `git http-backend` on the same machine: clone, push, 40-way push, grep cold/warm.
- **Done:** `BENCHMARKS.md` with scripts, one table, one chart, caveats. Grep first byte < 100 ms on a 500 MB repo.

### Week 6: Agent fleet demo + video + writeup
- Coordinator (TypeScript, Claude Agent SDK): takes `tasks.json`, forks 20 branches, spawns 20 agents.
  Each agent: grep + file-at-ref (no clone) -> edit -> test -> push. Merge queue rebases onto main.
  Measure per-agent setup: clone path vs API path.
- Remotion launch video (section 5). README quickstart. 2,000-word writeup on delta resolution or ref contention.
- **Done:** a stranger clones, runs `perry serve`, pushes in < 5 min. Video on YouTube. Bullets have final numbers.

**Cuts if behind:** drop S3 (week 4, keep SQLite + concurrency), drop `/tree` and webhooks (week 5),
cut agents from 20 to 5 (week 6). Never cut week 4 concurrency or week 5 benchmarks.

---

## 4. Benchmark targets (what the bullets will say)

| Metric | Target | Compare against |
|---|---|---|
| Concurrent agent pushes, zero loss | 40 | git http-backend (serializes on repo lock) |
| p99 push latency, 40 agents, 1 MB diff | < 500 ms | same |
| Agent setup: clone vs API read | 14 s -> < 200 ms | measured in demo |
| Grep first result, 500 MB repo, warm | < 100 ms | `git grep` after clone |
| Fork a branch | < 10 ms, 0 bytes | `git clone --shared` |

---

## 5. Launch video (Remotion, ~75 s, 1920x1080, 30 fps, YC launch style)

Style: dark background, one giant sentence per scene, terminal recordings, numbers that count up,
founder-voice narration. Music under narration no louder than -24 dB.

### Pipeline (audio-synced)
1. `scenes.ts` holds `{id, narration, visual}` per scene.
2. `generate-voiceover.ts` calls ElevenLabs per scene -> `public/voiceover/launch/<id>.mp3`
   (`node --env-file=.env --strip-types generate-voiceover.ts`; needs `ELEVENLABS_API_KEY` in `.env`).
3. `calculateMetadata` measures each MP3 with `getAudioDuration` and sets scene lengths and total
   `durationInFrames`, so visuals always match the voice. Scene = `<Sequence from={start} durationInFrames={len}>`.
4. Word captions: `@remotion/install-whisper-cpp` transcribes each MP3 -> `Caption[]` JSON ->
   `createTikTokStyleCaptions` for word-highlight subtitles.
5. Terminal clips recorded with `asciinema` + `agg` to MP4, placed with `<OffthreadVideo>`.
6. `npx remotion render Launch out/launch.mp4`.

### Scenes and narration

| # | id | Narration (voice) | Visual |
|---|---|---|---|
| 1 | hook | "Every coding agent starts the same way. It clones your repo. All of it. Every time." | Terminal: `git clone` progress bar crawling. Counter: 14.2 s |
| 2 | problem | "Run twenty agents and GitHub rate-limits you. Push from twenty branches and git serializes on a lock." | Grid of 20 agent tiles, half turn red with `429`, others queue |
| 3 | name | "Perry is a git server built for machines." | Logo. Giant text: "git, for fleets." |
| 4 | native | "It speaks native git. Clone, push, fetch. No wrapper around the git binary, no forge." | Terminal: `git push perry agent-07` succeeds. Architecture diagram fades in |
| 5 | api | "But agents don't need to clone. They grep. They read one file at one ref. Perry serves that over HTTP in under a hundred milliseconds." | Split screen: left `git clone` still crawling, right `curl /grep` returns instantly. Counter: 14.2 s vs 0.18 s |
| 6 | fleet | "Here are twenty Claude agents, twenty branches, one repo, pushing at once." | Screen recording of coordinator; 20 tiles go green; merge queue ticks |
| 7 | numbers | "Forty concurrent pushes. Zero lost updates. Forks cost nothing." | Three stat tiles counting up from 0 |
| 8 | open | "It's open source. One binary. SQLite on your laptop, S3 in prod." | `go install ...` then `perry serve` |
| 9 | cta | "Point your agents at Perry. Link below." | URL, GitHub stars widget, end card |

### Files
```
video/
  src/Root.tsx            Composition "Launch", calculateMetadata
  src/Launch.tsx          maps scenes -> <Sequence> + <Audio src={staticFile(...)}/>
  src/scenes/*.tsx        one component per scene id
  src/scenes.ts           narration + visual config
  generate-voiceover.ts   ElevenLabs
  transcribe.ts           whisper.cpp -> captions JSON
  public/voiceover/launch/*.mp3, public/clips/*.mp4
```

---

## 6. Resume bullets (fill numbers from BENCHMARKS.md)

- Built **Perry**, an open-source git-compatible object store for coding-agent fleets (Go): implemented
  the git smart HTTP protocol, packfile format and delta compression from scratch; sustained **40 concurrent
  agent pushes** with transactional compare-and-swap ref updates and zero data loss on SQLite and S3 backends.
- Designed a cloneless read API (grep, file-at-ref, zero-copy branch forks) that cut per-agent repo setup
  from **14 s to under 200 ms**; ran **20 parallel Claude agents** against one repo with an automated merge queue.
- Published benchmarks vs `git http-backend`, a technical writeup on delta resolution and ref contention,
  and a launch video; **N stars / M users** in the first month.

---

## 7. First commands

```
mkdir perry && cd perry && go mod init github.com/<you>/perry
git clone --bare https://github.com/spf13/cobra testdata/cobra.git   # pack fixtures
npx create-video@latest video --template blank
cd video && npm i @remotion/captions @remotion/install-whisper-cpp @remotion/media-utils
```

Reference reading: git source `Documentation/gitformat-pack.txt`, `gitprotocol-pack.txt`, `gitprotocol-http.txt`;
go-git `plumbing/format/packfile` for cross-checking; code.storage docs for the API shape to match.
