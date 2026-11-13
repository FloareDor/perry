package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
)

// ---------------------------------------------------------------------------
// The end-to-end agent task.
//
// Everything else in this package measures one operation: a setup, a grep, a
// push. That leaves the obvious objection unanswered -- a faster git operation
// is not a faster agent. This measures the whole task instead:
//
//	find the symbol, read the file it lives in, edit it, publish the edit
//
// which is the smallest unit of work an agent actually performs. The clock
// starts with nothing on disk and stops once the change is durable on the
// server, so setup is on the clock for every path that needs it.
//
// Two things make this different from the setup comparison:
//
// Real transport on both sides. The git baselines run over real smart HTTP
// through `git http-backend`, not over file://. Elsewhere in this package
// file:// is used deliberately to be generous to git, but file:// has no
// network at all, so it cannot answer a question about latency.
//
// Injected round-trip latency. Agents run in a datacentre and their git host
// is somewhere else; loopback flatters whoever makes fewer round trips, which
// here is perry. Measuring at 0ms and then at 25ms and 75ms shows how much of
// any difference is round-trip count rather than work done.
// ---------------------------------------------------------------------------

// netShaper is an HTTP proxy that adds latency and counts what crosses it.
//
// Every request pays rtt once, before it reaches the server, which models one
// round trip. It does not model bandwidth, TCP slow start or congestion, all
// of which punish large transfers further, so a path that moves more bytes is
// treated more kindly here than it would be on a real link.
//
// It also counts payload bytes in both directions. That is what makes this
// comparison wire-to-wire: git's clone and push and perry's API calls are
// measured by the same counter on the same socket, rather than by asking each
// system what it thinks it transferred.
type netShaper struct {
	srv  *httptest.Server
	rtt  time.Duration
	up   atomic.Int64 // request payload bytes
	down atomic.Int64 // response payload bytes
	reqs atomic.Int64
}

func newNetShaper(t testing.TB, target string, rtt time.Duration) *netShaper {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	ns := &netShaper{rtt: rtt}

	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
			r.Host = u.Host
			if r.Body != nil {
				r.Body = &countingReadCloser{rc: r.Body, n: &ns.up}
			}
			ns.reqs.Add(1)
			if ns.rtt > 0 {
				time.Sleep(ns.rtt)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Body = &countingReadCloser{rc: resp.Body, n: &ns.down}
			return nil
		},
		// Streaming matters: git's smart HTTP sends a chunked body and
		// expects progress, so buffering the whole response would change
		// the shape of what is being measured.
		FlushInterval: -1,
	}
	ns.srv = httptest.NewServer(proxy)
	t.Cleanup(ns.srv.Close)
	return ns
}

func (ns *netShaper) reset() {
	ns.up.Store(0)
	ns.down.Store(0)
	ns.reqs.Store(0)
}

func (ns *netShaper) wire() int64 { return ns.up.Load() + ns.down.Load() }

type countingReadCloser struct {
	rc io.ReadCloser
	n  *atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

// gitHTTPBackend serves every bare repository under root over real smart
// HTTP, the same way a git host does. Without this the git baselines could
// only be measured over file://, which has no round trips to add latency to.
func gitHTTPBackend(t testing.TB, root string) *httptest.Server {
	t.Helper()
	backend := filepath.Join(gitExecPath(t), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		if _, err2 := os.Stat(backend + ".exe"); err2 != nil {
			t.Skipf("git-http-backend not found at %s; the git baselines cannot be served over HTTP", backend)
		}
		backend += ".exe"
	}
	h := &cgi.Handler{
		Path: backend,
		Root: "/",
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			// Every repository under root is a fixture created by this test.
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func gitExecPath(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command(bin, "--exec-path")
	cmd.Env = gitEnvB()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// forkOrigin makes a private bare copy of a corpus for one variant to push
// to, so variants cannot interfere with each other or inherit each other's
// commits. A local clone hardlinks its objects, so this is cheap, and it
// happens off the clock.
func forkOrigin(t testing.TB, src, root, name string) string {
	t.Helper()
	gitB(t, root, "clone", "-q", "--bare", src, name)
	dst := filepath.Join(root, name)
	// http-backend refuses a push unless the receiving repository opts in,
	// and refuses to update the checked-out branch of a non-bare one.
	gitB(t, dst, "config", "http.receivepack", "true")
	gitB(t, dst, "config", "uploadpack.allowFilter", "true")

	// Perry serves the repository as main whatever upstream calls its default
	// branch, so the fork is renamed to match. Without this the agent's push
	// would *create* refs/heads/main next to an untouched master, which is a
	// different and cheaper operation than updating the branch it cloned.
	head := strings.TrimPrefix(gitB(t, dst, "symbolic-ref", "HEAD"), "refs/heads/")
	if head != "main" {
		gitB(t, dst, "branch", "-m", head, "main")
	}
	return dst
}

type taskResult struct {
	name     string
	dur      time.Duration
	wire     int64
	disk     int64
	reqs     int64
	complete bool
	note     string
}

// TestAgentTaskEndToEnd is the measurement the earlier benchmarks could not
// make: not how fast one git operation runs, but how long an agent waits
// between being given a task and having the edit published.
func TestAgentTaskEndToEnd(t *testing.T) {
	requireBench(t)
	for _, spec := range realRepos {
		for _, rtt := range []time.Duration{0, 25 * time.Millisecond, 75 * time.Millisecond} {
			name := fmt.Sprintf("%s/rtt%dms", spec.label, rtt.Milliseconds())
			t.Run(name, func(t *testing.T) {
				runAgentTask(t, spec, rtt)
			})
		}
	}
}

func runAgentTask(t *testing.T, spec repoSpec, rtt time.Duration) {
	c := buildRealCorpus(t, spec)

	// One directory holding a private bare repository per git variant, all
	// served by one http-backend behind one shaper.
	gitRoot := t.TempDir()
	gitSrv := gitHTTPBackend(t, gitRoot)
	gitNet := newNetShaper(t, gitSrv.URL, rtt)
	perryNet := newNetShaper(t, c.ts.URL, rtt)

	// A unique marker, so "did the edit land" is answered by reading the
	// published content back rather than by trusting an exit code.
	marker := fmt.Sprintf("// perry-bench %d\n", time.Now().UnixNano())
	var results []taskResult

	// gitTask is the whole agent task over real HTTP: clone, find, read,
	// edit, commit, push. cloneArgs is what distinguishes the variants.
	gitTask := func(name, variant, note string, sparse bool, cloneArgs ...string) {
		origin := forkOrigin(t, c.origin, gitRoot, variant+".git")
		remote := gitNet.srv.URL + "/" + variant + ".git"
		work := filepath.Join(t.TempDir(), "work")

		gitNet.reset()
		start := time.Now()

		args := append([]string{"clone", "-q"}, cloneArgs...)
		args = append(args, remote, work)
		gitB(t, filepath.Dir(work), args...)
		if sparse {
			gitB(t, work, "sparse-checkout", "set", spec.sparse)
		}

		// Find the file. A sparse checkout only has one directory, so the
		// search is given the answer; the others have to look everywhere.
		grepArgs := []string{"grep", "-n", "-l", spec.needle}
		if sparse {
			grepArgs = append(grepArgs, "--", spec.sparse)
		}
		found := gitB(t, work, grepArgs...)
		rel := strings.SplitN(strings.TrimSpace(found), "\n", 2)[0]

		// Read it, edit it, publish it.
		abs := filepath.Join(work, filepath.FromSlash(rel))
		prev, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("%s: reading %s: %v", name, rel, err)
		}
		if err := os.WriteFile(abs, append(prev, marker...), 0o644); err != nil {
			t.Fatal(err)
		}
		gitB(t, work, "add", "--", rel)
		gitB(t, work, "commit", "-q", "-m", "agent edit")
		gitB(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

		dur := time.Since(start)

		// Verify against the server, not the client.
		published := gitB(t, origin, "show", "main:"+rel)
		results = append(results, taskResult{
			name: name, dur: dur, wire: gitNet.wire(), disk: treeBytes(work),
			reqs: gitNet.reqs.Load(), note: note,
			complete: strings.Contains(published, strings.TrimSpace(marker)),
		})
	}

	gitTask("full clone", "full", "what an agent does today", false)
	gitTask("shallow --depth 1", "shallow", "typical ephemeral agent", false,
		"--depth", "1")
	gitTask("shallow + sparse (dir known)", "sparse", "git's best case; given the answer", true,
		"--depth", "1", "--filter=blob:none", "--sparse")

	// Warm worktree: the repository is already on disk, but a warm clone is
	// stale, so fetching is on the clock. Skipping that would measure a
	// worktree against a repository nobody had to keep current.
	{
		origin := forkOrigin(t, c.origin, gitRoot, "warm.git")
		remote := gitNet.srv.URL + "/warm.git"
		warmParent := t.TempDir()
		gitB(t, warmParent, "clone", "-q", remote, "warm") // off the clock
		warm := filepath.Join(warmParent, "warm")
		wt := filepath.Join(t.TempDir(), "wt")

		gitNet.reset()
		start := time.Now()
		gitB(t, warm, "fetch", "-q", "origin")
		gitB(t, warm, "worktree", "add", "-q", "--detach", wt, "FETCH_HEAD")
		rel := strings.SplitN(strings.TrimSpace(gitB(t, wt, "grep", "-n", "-l", spec.needle)), "\n", 2)[0]
		abs := filepath.Join(wt, filepath.FromSlash(rel))
		prev, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("warm worktree: reading %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, append(prev, marker...), 0o644); err != nil {
			t.Fatal(err)
		}
		gitB(t, wt, "add", "--", rel)
		gitB(t, wt, "commit", "-q", "-m", "agent edit")
		gitB(t, wt, "push", "-q", "origin", "HEAD:refs/heads/main")
		dur := time.Since(start)

		published := gitB(t, origin, "show", "main:"+rel)
		results = append(results, taskResult{
			name: "warm worktree + fetch", dur: dur, wire: gitNet.wire(),
			disk: treeBytes(wt), reqs: gitNet.reqs.Load(),
			note:     "needs a prior clone kept on disk",
			complete: strings.Contains(published, strings.TrimSpace(marker)),
		})
	}

	// Perry: grep the server, read the one file that matched, commit it back.
	// No checkout, and nothing on disk at any point.
	{
		base := perryNet.srv.URL + "/v1/repos/bench"
		cl := &http.Client{}

		perryNet.reset()
		start := time.Now()

		rel, tip := perryFind(t, cl, base, spec.needle)
		prev := perryRead(t, cl, base, rel)
		body, err := json.Marshal(api.CommitRequest{
			Branch:  "main",
			Message: "agent edit",
			Expect:  tip,
			Changes: []api.Change{{Path: rel, Content: append(append([]byte{}, prev...), marker...)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := cl.Post(base+"/commit", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if code != http.StatusOK {
			t.Fatalf("perry commit: HTTP %d", code)
		}
		dur := time.Since(start)

		published, err := api.ReadFile(context.Background(), c.store, "main", rel)
		if err != nil {
			t.Fatalf("reading %s back: %v", rel, err)
		}
		results = append(results, taskResult{
			name: "perry API", dur: dur, wire: perryNet.wire(), disk: 0,
			reqs:     perryNet.reqs.Load(),
			note:     "no checkout, nothing on disk",
			complete: bytes.Contains(published, []byte(strings.TrimSpace(marker))),
		})
	}

	t.Logf("corpus %s: %d commits, %d files, %s packed; needle %q",
		spec.label, c.commits, c.files, humanBytes(c.packed), spec.needle)
	t.Logf("task: locate the needle, read its file, append a line, publish it")
	t.Logf("added round-trip latency: %v per request, both sides, same proxy", rtt)
	t.Logf("%-30s %10s %11s %11s %6s  %s", "path", "time", "wire", "disk", "reqs", "note")
	for _, r := range results {
		status := ""
		if !r.complete {
			status = "  [EDIT DID NOT LAND]"
		}
		t.Logf("%-30s %10v %11s %11s %6d  %s%s",
			r.name, r.dur.Round(time.Millisecond),
			humanBytes(r.wire), humanBytes(r.disk), r.reqs, r.note, status)
	}

	// Every path has to actually finish the task, or its time means nothing.
	for _, r := range results {
		if !r.complete {
			t.Errorf("%s did not publish the edit", r.name)
		}
	}
}

// perryFind greps the server and returns the first matching path along with
// the tip the search ran against, which the commit pins with Expect.
func perryFind(t *testing.T, cl *http.Client, base, needle string) (string, string) {
	t.Helper()

	resp, err := cl.Get(base + "/grep?q=" + url.QueryEscape(needle) + "&ref=main&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding grep response: %v", err)
	}
	if len(out.Matches) == 0 {
		t.Fatalf("perry grep found nothing for %q", needle)
	}

	r2, err := cl.Get(base + "/refs?prefix=refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var refs struct {
		Refs []struct {
			Name   string `json:"name"`
			Target string `json:"target"`
		} `json:"refs"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&refs); err != nil {
		t.Fatalf("decoding refs response: %v", err)
	}
	for _, r := range refs.Refs {
		if r.Name == "refs/heads/main" {
			return out.Matches[0].Path, r.Target
		}
	}
	t.Fatal("perry: refs/heads/main not found")
	return "", ""
}

func perryRead(t *testing.T, cl *http.Client, base, path string) []byte {
	t.Helper()
	resp, err := cl.Get(base + "/file?ref=main&path=" + url.QueryEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("perry file %s: HTTP %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
