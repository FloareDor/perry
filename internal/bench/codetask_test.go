package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/server"
	"github.com/perry-dev/perry/internal/store"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

// Real coding tasks: edit, build, test.
//
// Every other agent benchmark in this package measures repository
// operations -- find a file, edit a line, commit. None of them needs the
// result to compile, and none of them needs the agent to run anything. That
// is the easy half of an agent's day, and it is also the half perry was
// designed for, so a suite made only of it flatters perry by construction.
//
// This file poses the other half. Each task is a real defect or a real
// missing feature in a small Go module, stated the way a developer would
// state it, and an attempt counts as finished only when the harness itself
// clones the agent's commit into a clean directory and `go test ./...`
// passes there. The agent's own report of success is not consulted.
//
// The point is not that perry wins. Perry has no execution: an agent holding
// only perry's tools cannot compile anything, cannot run a test, and has to
// write code correctly without ever seeing it fail. That limitation has been
// asserted in BENCHMARKS.md since the beginning and never measured. It is
// measured here, against a git checkout that is already on disk -- the warm
// worktree an agent platform with a cache would really have, and the hardest
// baseline in the package.

// codeTaskTimeout is longer than the other suites allow. These tasks have a
// build-test-edit loop in them, and a model that is iterating against a
// failing test is working, not stuck.
const codeTaskTimeout = 10 * time.Minute

// calcFixture is the module every task is posed against: a lexer, a parser
// and an evaluator for arithmetic expressions, about 400 lines across three
// packages, with a test suite that passes before the task is seeded. It
// depends on nothing outside the standard library, so `go test ./...` is
// hermetic and takes under a second -- which is what makes it usable as a
// benchmark fixture at all.
func calcFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "bench", "testdata", "calc")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("the calc fixture is missing from %s", dir)
	}
	return dir
}

// ---------------------------------------------------------------------------
// The tasks.
// ---------------------------------------------------------------------------

type codeTask struct {
	name string
	what string

	// seed mutates a fresh copy of the fixture into the state the agent
	// finds it in: a bug introduced, or a test added for something that is
	// not implemented. After seeding, `go test ./...` must fail -- the
	// harness checks that, because a task whose tests already pass would be
	// completed by every arm doing nothing.
	seed func(dir string) error

	// goal is posed to every arm in identical words.
	goal string
}

func codeTasks() []codeTask {
	return []codeTask{
		{
			name: "fix-failing-test",
			what: "diagnose a failing test and fix the source it blames",
			seed: func(dir string) error {
				// Division becomes right associative, so 8/4/2 is 4 rather
				// than 1. The failing test is in eval; the defect is in
				// parse, one package away, which is the point: the output
				// does not say where to look.
				return replaceOnce(filepath.Join(dir, "parse", "parse.go"),
					"\t\top := p.next().Kind.String()\n\t\tr, err := p.unary()\n",
					"\t\top := p.next().Kind.String()\n\t\tr, err := p.term()\n")
			},
			goal: `The tests in this Go module are failing.

1. Work out why ` + "`go test ./...`" + ` fails.
2. Fix the source so the whole suite passes.

The tests are correct and the source is wrong. Do not change any file whose
name ends in _test.go.`,
		},
		{
			name: "add-operator",
			what: "implement an operator end to end across three packages",
			seed: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "eval", "modulo_test.go"),
					[]byte(moduloTest), 0o644)
			},
			goal: `This Go module lexes, parses and evaluates arithmetic expressions. A
test for the remainder operator ` + "`%`" + ` has been added and it fails,
because ` + "`%`" + ` is not implemented anywhere.

Implement it so that ` + "`go test ./...`" + ` passes. It binds as tightly as
` + "`*`" + ` and ` + "`/`" + ` and associates to the left, the same way they
do.

Do not change any file whose name ends in _test.go.`,
		},
		{
			name: "error-offsets",
			what: "thread a value through two packages to improve error messages",
			seed: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "eval", "errpos_test.go"),
					[]byte(errposTest), 0o644)
			},
			goal: `This Go module lexes, parses and evaluates arithmetic expressions. A
test has been added that requires every lexing and parsing error to say where
in the input it went wrong, as "at offset N", counting bytes from zero. It
fails, because the errors do not say that today.

Make ` + "`go test ./...`" + ` pass.

Do not change any file whose name ends in _test.go.`,
		},
	}
}

const moduloTest = `package eval_test

import (
	"math"
	"testing"

	"example.com/calc/eval"
)

// TestModulo covers the remainder operator, which binds as tightly as * and
// / and associates to the left like them.
func TestModulo(t *testing.T) {
	cases := []struct {
		src  string
		want float64
	}{
		{"7 % 3", 1},
		{"10 % 4", 2},
		{"2 + 9 % 4", 3},
		{"9 % 4 * 2", 2},
		{"20 % 7 % 2", 0},
		{"-7 % 3", -1},
		{"(10 + 4) % 5", 4},
	}
	for _, c := range cases {
		got, err := eval.Eval(c.src)
		if err != nil {
			t.Errorf("Eval(%q): %v", c.src, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Eval(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}
`

const errposTest = `package eval_test

import (
	"strings"
	"testing"

	"example.com/calc/eval"
)

// TestErrorPosition requires every lexing and parsing error to say where in
// the input it happened, as "at offset N", counting bytes from zero.
func TestErrorPosition(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"1 @ 2", "at offset 2"},
		{"1 +", "at offset 3"},
		{"(1 + 2", "at offset 6"},
		{"1 + * 2", "at offset 4"},
		{"3 4", "at offset 2"},
	}
	for _, c := range cases {
		_, err := eval.Eval(c.src)
		if err == nil {
			t.Errorf("Eval(%q) returned no error", c.src)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("Eval(%q) said %q, want it to contain %q", c.src, err, c.want)
		}
	}
}
`

// ---------------------------------------------------------------------------
// Verification: the harness builds and tests the agent's commit itself.
// ---------------------------------------------------------------------------

// codeTaskCheck is the single check every task in this file is graded by.
//
// It reads the finished repository back through a path the agent did not
// write through, writes it into an empty directory, and runs the suite. A
// model that reports success, or that edits the tests until they agree with
// it, fails here. So does one that commits something that does not compile:
// the other suites in this package could not tell that apart from a correct
// answer, because they never built anything.
func codeTaskCheck(t *testing.T, baseTests map[string]string) func(repoReader) error {
	return func(r repoReader) error {
		tree, err := wholeTree(r)
		if err != nil {
			return fmt.Errorf("reading the finished repository: %v", err)
		}

		// The tests are part of the task statement, not part of the answer.
		for path, want := range baseTests {
			got, ok := tree[path]
			if !ok {
				return fmt.Errorf("%s was deleted; the tests are not the agent's to change", path)
			}
			if got != want {
				return fmt.Errorf("%s was modified; the tests are not the agent's to change", path)
			}
		}
		for path := range tree {
			if strings.HasSuffix(path, "_test.go") && baseTests[path] == "" {
				return fmt.Errorf("%s is a test file the agent added; the suite it has to pass is the one it was given", path)
			}
		}

		dir := t.TempDir()
		for path, body := range tree {
			full := filepath.Join(dir, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				return err
			}
		}

		out, err := goTest(dir)
		if err != nil {
			return fmt.Errorf("go test failed on the committed tree: %s",
				firstLine(strings.TrimSpace(lastLines(out, 6))))
		}
		return nil
	}
}

// wholeTree reads every file at the revision an arm published to.
//
// The two readers are kept apart on purpose -- the perry arms are read out
// of the object store and the git arms out of their bare origin -- so a type
// switch is the honest way to ask both for the same thing without giving
// either a path back through its own write.
func wholeTree(r repoReader) (map[string]string, error) {
	out := map[string]string{}
	switch v := r.(type) {
	case *perryReader:
		paths, err := api.ListFiles(context.Background(), v.c.store, v.branch, "")
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			b, err := api.ReadFile(context.Background(), v.c.store, v.branch, p)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", p, err)
			}
			out[p] = string(b)
		}
	case *gitReader:
		listing, err := gitQuiet(v.dir, "ls-tree", "-r", "--name-only", v.ref)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(strings.TrimSpace(listing), "\n") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			body, err := gitQuiet(v.dir, "show", v.ref+":"+p)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", p, err)
			}
			out[p] = body
		}
	default:
		return nil, fmt.Errorf("no tree reader for %T", r)
	}
	return out, nil
}

// goTest runs the fixture's suite in dir. The module has no dependencies, so
// this needs no network and nothing in the module cache.
func goTest(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	// GOFLAGS is cleared because whatever the developer running the harness
	// has set for perry's own build has no business reaching the fixture.
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// The corpus: the fixture, seeded for one task, in perry and in git.
// ---------------------------------------------------------------------------

// buildCalcCorpus seeds the fixture for one task and publishes it both ways.
// It returns the corpus and the test files as the agent will find them, so
// verification can tell whether they survived.
func buildCalcCorpus(t *testing.T, task codeTask) (*corpus, map[string]string) {
	t.Helper()

	src := filepath.Join(t.TempDir(), "calc")
	if err := copyTree(calcFixture(t), src); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
	if err := task.seed(src); err != nil {
		t.Fatalf("seeding %s: %v", task.name, err)
	}

	// A task whose suite already passes would be finished by every arm
	// doing nothing at all, and would quietly report four successes. The
	// seed is therefore checked rather than trusted.
	if out, err := goTest(src); err == nil {
		t.Fatalf("%s: the seeded fixture still passes its tests, so the task is empty:\n%s", task.name, out)
	}

	baseTests := map[string]string{}
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		baseTests[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("reading the fixture's tests: %v", err)
	}

	gitB(t, src, "init", "-q")
	gitB(t, src, "symbolic-ref", "HEAD", "refs/heads/main")
	gitB(t, src, "add", "-A")
	gitB(t, src, "commit", "-q", "-m", "calc: a small expression evaluator")

	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	gitB(t, base, "clone", "-q", "--bare", src, "origin.git")
	gitB(t, origin, "config", "uploadpack.allowFilter", "true")
	gitB(t, origin, "config", "uploadpack.allowAnySHA1InWant", "true")

	ts, open := perryTestServer(t)
	start := time.Now()
	gitB(t, origin, "push", "-q", ts.URL+"/bench.git", "main:refs/heads/main")
	ingest := time.Since(start)

	warmParent := t.TempDir()
	gitB(t, warmParent, "clone", "-q", fileURL(origin), "warm")

	s, err := open("bench")
	if err != nil {
		t.Fatal(err)
	}
	commits, _ := strconv.Atoi(gitB(t, origin, "rev-list", "--count", "main"))
	files := len(strings.Split(gitB(t, origin, "ls-tree", "-r", "--name-only", "main"), "\n"))

	return &corpus{
		spec: repoSpec{dir: "calc", label: "calc",
			needle: "func (p *parser) term()", sparse: "parse"},
		ts: ts, store: s, origin: origin,
		warm:    filepath.Join(warmParent, "warm"),
		commits: commits, files: files,
		packed: packBytes(t, origin), ingest: ingest,
	}, baseTests
}

// perryTestServer starts a SQLite-backed perry serving both the git wire
// protocol and the HTTP API, the same pairing buildRealCorpus uses.
func perryTestServer(t *testing.T) (*httptest.Server, func(string) (store.Store, error)) {
	t.Helper()
	dir := t.TempDir()
	var mu sync.Mutex
	stores := map[string]store.Store{}
	open := func(name string) (store.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := stores[name]; ok {
			return s, nil
		}
		s, err := sqlite.Open(filepath.Join(dir, name+".db"))
		if err != nil {
			return nil, err
		}
		stores[name] = s
		return s, nil
	}

	gitSrv := server.New(open)
	gitSrv.Logger = nil
	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", &api.Handler{Open: open})
	mux.Handle("/", gitSrv)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		for _, s := range stores {
			_ = s.Close()
		}
	})
	return ts, open
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func replaceOnce(path, old, replacement string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(b)
	if n := strings.Count(s, old); n != 1 {
		return fmt.Errorf("%s: the anchor appears %d times, want exactly 1", path, n)
	}
	return os.WriteFile(path, []byte(strings.Replace(s, old, replacement, 1)), 0o644)
}

// ---------------------------------------------------------------------------
// Two arms the other suites do not have.
// ---------------------------------------------------------------------------

// perrySandboxArm is perry paired with somewhere to run the tests.
//
// Perry has no execution and is not going to grow any: it stores and serves
// code. An agent that has to make a test pass therefore needs a working tree
// from somewhere, and the realistic arrangement is this one -- edits go to
// the server through perry_commit, and a checkout exists alongside purely to
// compile and run. The clone is on the clock, because the developer waiting
// for the agent waits for it too, and perry serves a full clone: it has no
// shallow or partial fetch, so this arm pays perry's worst case.
func perrySandboxArm(t *testing.T, bin string, c *corpus, perryBin string,
	net *netShaper, j job, results string) agentRun {

	run := agentRun{arm: "perry + test sandbox", rtt: j.rtt, trial: j.trial, task: j.task}
	branch := "agent/sandbox-" + j.tag

	// Off the clock, to match the git arms: each of those gets its own fork
	// of the origin before its timer starts.
	base, err := api.Fork(context.Background(), c.store, "main", branch)
	if err != nil {
		t.Fatalf("preparing %s: %v", branch, err)
	}

	work := t.TempDir()
	cfgPath := filepath.Join(work, "mcp.json")
	cfg := map[string]any{"mcpServers": map[string]any{
		"perry": map[string]any{
			"command": perryBin,
			"args":    []string{"mcp", "-server", net.srv.URL, "-repo", "bench"},
		},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	net.reset()
	start := time.Now()
	gitB(t, work, "clone", "-q", "--branch", branch, net.srv.URL+"/bench.git", "sandbox")
	run.setup = time.Since(start)

	prompt := fmt.Sprintf(`You are working on a Go repository hosted on a perry server.

Every edit you make must go through the perry_* tools, which commit straight to the server on the branch %q. There is no checkout for you to edit.

There is a clone of that branch in the ./sandbox directory. It is there so that you can compile and run the tests, and for nothing else. Do not edit anything in it, and do not commit or push with git: changes made there are thrown away and will not count.

The loop is:
  1. Make an edit with perry_commit on %q.
  2. Bring the sandbox up to date:  git -C sandbox pull -q
  3. Run the tests:                 cd sandbox && go test ./...

Task:
%s

When you are done, report the paths you changed and the commit id.`,
		branch, branch, j.goal)

	res, elapsed, err := runClaude(t, bin, work, prompt, []string{
		"--mcp-config", cfgPath,
		"--allowed-tools", strings.Join([]string{
			"mcp__perry__perry_grep",
			"mcp__perry__perry_read_file",
			"mcp__perry__perry_list_files",
			"mcp__perry__perry_list_tree",
			"mcp__perry__perry_list_branches",
			"mcp__perry__perry_create_branch",
			"mcp__perry__perry_commit",
			"Bash",
		}, ","),
		"--permission-mode", "acceptEdits",
	}, filepath.Join(results, "perrysandbox-"+j.tag+".json"))

	run.total = time.Since(start)
	run.wire, run.reqs = net.wire(), net.reqs.Load()
	run.disk = treeBytes(filepath.Join(work, "sandbox"))

	if err != nil {
		run.note = err.Error()
		run.agent = elapsed
		return run
	}
	fillFromClaude(&run, res)

	if err := j.verify(&perryReader{c: c, branch: branch, base: base.String()}); err != nil {
		if run.note == "" {
			run.note = err.Error()
		}
		return run
	}
	run.landed = true
	return run
}

// gitWarmArm is the baseline the published numbers were missing: a full
// checkout that is already on disk when the task arrives.
//
// Every git arm elsewhere in this package clones from nothing, which is the
// comparison that flatters perry most, because perry's whole advantage is
// that it never clones. A platform that keeps repository caches does not pay
// that cost twice, and this arm does not either: the clone happens off the
// clock and the only setup charged is bringing it up to date, which is what
// a warm cache actually costs.
func gitWarmArm(t *testing.T, bin string, c *corpus, gitRoot string,
	net *netShaper, j job, results string) agentRun {

	run := agentRun{arm: "git warm worktree", rtt: j.rtt, trial: j.trial, task: j.task}

	name := "warm-" + j.tag + ".git"
	origin := forkOrigin(t, c.origin, gitRoot, name)
	gitBase, _ := gitQuiet(origin, "rev-parse", "main")

	// Off the clock. The premise of this arm is that the tree is already
	// there, so the clone that produced it is not the agent's to pay for.
	parent := t.TempDir()
	gitB(t, parent, "clone", "-q", fileURL(origin), "work")
	work := filepath.Join(parent, "work")
	gitB(t, work, "remote", "set-url", "origin", net.srv.URL+"/"+name)
	gitB(t, work, "config", "user.email", "agent@perry.local")
	gitB(t, work, "config", "user.name", "perry-agent")

	net.reset()
	start := time.Now()
	// On the clock. A cache still has to find out whether it is stale.
	gitB(t, work, "pull", "-q", "--ff-only")
	run.setup = time.Since(start)

	res, elapsed, err := runClaude(t, bin, work, gitAgentPrompt(j.goal, ""), []string{
		"--allowed-tools", "Bash,Read,Edit,Write,Glob,Grep",
		"--permission-mode", "acceptEdits",
	}, filepath.Join(results, "warm-"+j.tag+".json"))

	run.total = time.Since(start)
	run.wire, run.reqs = net.wire(), net.reqs.Load()
	run.disk = treeBytes(work)

	if err != nil {
		run.note = err.Error()
		run.agent = elapsed
		return run
	}
	fillFromClaude(&run, res)

	if err := j.verify(&gitReader{dir: origin, ref: "main", base: strings.TrimSpace(gitBase)}); err != nil {
		if run.note == "" {
			run.note = err.Error()
		}
		return run
	}
	run.landed = true
	return run
}

// ---------------------------------------------------------------------------
// The suite.
// ---------------------------------------------------------------------------

// TestCodeTaskSuite poses each coding task to four arms and grades them by
// building and testing what they committed.
func TestCodeTaskSuite(t *testing.T) {
	bin := requireAgentBench(t)
	if os.Getenv("PERRY_CODE_EVAL") == "" {
		t.Skip("the real-coding-task suite is opt in, and costs money. Run it with:\n" +
			"\tPERRY_BENCH=1 PERRY_AGENT_BENCH=1 PERRY_CODE_EVAL=1 go test ./internal/bench -run CodeTaskSuite -v -timeout 180m")
	}

	// These tasks iterate against a failing test, which the other suites
	// never do, so the per-task budget is larger.
	restore := agentTimeout
	agentTimeout = codeTaskTimeout
	t.Cleanup(func() { agentTimeout = restore })

	perryBin := filepath.Join(t.TempDir(), "perry"+exeSuffix())
	build := exec.Command("go", "build", "-o", perryBin, "github.com/perry-dev/perry/cmd/perry")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building perry: %v\n%s", err, out)
	}

	results := filepath.Join(repoRoot(t), "testdata", "agent-runs")
	if err := os.MkdirAll(results, 0o755); err != nil {
		t.Fatal(err)
	}

	// Latency is held at zero. The single-task comparison already measured
	// what latency does to each transport, and the question here is whether
	// the task gets finished at all.
	rtt := time.Duration(0)

	// More than one attempt per task, because a coding task has a right
	// answer and a model does not always find it: one attempt reports a
	// coin flip as a capability. Completion is counted over every attempt.
	trials := envInt("PERRY_CODE_TRIALS", 2)

	var runs []agentRun
	for _, task := range selectedCodeTasks(t) {
		c, baseTests := buildCalcCorpus(t, task)
		t.Logf("  calc/%s: %s", task.name, task.what)

		gitRoot := t.TempDir()
		gitSrv := gitHTTPBackend(t, gitRoot)
		gitNet := newNetShaper(t, gitSrv.URL, rtt)
		perryNet := newNetShaper(t, c.ts.URL, rtt)
		sandboxNet := newNetShaper(t, c.ts.URL, rtt)

		for trial := 1; trial <= trials; trial++ {
			j := job{
				task:   "calc/" + task.name,
				tag:    fmt.Sprintf("%s-t%d", task.name, trial),
				goal:   task.goal,
				trial:  trial,
				rtt:    rtt,
				verify: codeTaskCheck(t, baseTests),
			}

			runs = append(runs,
				perryArm(t, bin, c, perryBin, perryNet, j, results),
				perrySandboxArm(t, bin, c, perryBin, sandboxNet, j, results),
				gitWarmArm(t, bin, c, gitRoot, gitNet, j, results),
				gitArm(t, bin, c, gitRoot, gitNet, j, results,
					"git shallow (cold)", "shallow", false,
					"--depth", "1"),
			)
		}
	}

	reportCodeTasks(t, runs)

	landed := 0
	for _, r := range runs {
		if r.landed {
			landed++
		}
	}
	if landed == 0 {
		t.Fatalf("no arm finished any of %d runs; the harness measured nothing", len(runs))
	}
}

func selectedCodeTasks(t *testing.T) []codeTask {
	all := codeTasks()
	want := os.Getenv("PERRY_CODE_TASKS")
	if want == "" {
		return all
	}
	keep := map[string]bool{}
	for _, n := range strings.Split(want, ",") {
		keep[strings.TrimSpace(n)] = true
	}
	var out []codeTask
	for _, task := range all {
		if keep[task.name] {
			out = append(out, task)
		}
	}
	if len(out) == 0 {
		t.Fatalf("PERRY_CODE_TASKS=%q matched none of the %d tasks", want, len(all))
	}
	return out
}

// reportCodeTasks prints completion first. On these tasks it is close to the
// only thing worth printing: an arm that cannot run the tests either guessed
// right or did not, and how fast it guessed is beside the point.
func reportCodeTasks(t *testing.T, runs []agentRun) {
	t.Log("")
	trials := 0
	for i := range runs {
		if runs[i].trial > trials {
			trials = runs[i].trial
		}
	}
	t.Logf("real coding tasks, graded by building and testing the commit: %s, %d attempts per task per arm",
		agentModel, trials)
	t.Log("")

	arms := armOrder(runs)

	t.Log("per task, did `go test ./...` pass on what the arm committed:")
	header := fmt.Sprintf("%-22s", "task")
	for _, a := range arms {
		header += fmt.Sprintf(" %-16s", shortCodeArm(a))
	}
	t.Log(header)

	for _, task := range taskOrder(runs) {
		line := fmt.Sprintf("%-22s", task)
		for _, a := range arms {
			var passed, n int
			var best []agentRun
			for i := range runs {
				if runs[i].task != task || runs[i].arm != a {
					continue
				}
				n++
				if runs[i].landed {
					passed++
					best = append(best, runs[i])
				}
			}
			switch {
			case n == 0:
				line += fmt.Sprintf(" %-16s", "-")
			case passed == 0:
				line += fmt.Sprintf(" %-16s", fmt.Sprintf("0/%d", n))
			default:
				med := medianDur(best, func(r agentRun) time.Duration { return r.total })
				line += fmt.Sprintf(" %-16s", fmt.Sprintf("%d/%d  %5.1fs", passed, n, med.Seconds()))
			}
		}
		t.Log(line)
	}

	t.Log("")
	t.Log("totals:")
	t.Logf("%-22s %8s %10s %10s %10s %12s %10s",
		"arm", "passed", "median", "setup", "cost/task", "wire/task", "disk")
	for _, a := range arms {
		var passed, n int
		var landedRuns []agentRun
		var cost float64
		var wire, disk int64
		for i := range runs {
			if runs[i].arm != a {
				continue
			}
			n++
			cost += runs[i].costUSD
			wire += runs[i].wire
			if runs[i].disk > disk {
				disk = runs[i].disk
			}
			if runs[i].landed {
				passed++
				landedRuns = append(landedRuns, runs[i])
			}
		}
		if n == 0 {
			continue
		}
		median := time.Duration(0)
		if len(landedRuns) > 0 {
			median = medianDur(landedRuns, func(r agentRun) time.Duration { return r.total })
		}
		t.Logf("%-22s %4d/%-3d %9s %10s %10.4f %12s %10s",
			a, passed, n,
			median.Round(time.Millisecond),
			medianDur(runsFor(runs, a), func(r agentRun) time.Duration { return r.setup }).Round(time.Millisecond),
			cost/float64(n), humanBytes(wire/int64(n)), humanBytes(disk))
	}

	var failures []string
	for i := range runs {
		if !runs[i].landed {
			failures = append(failures, fmt.Sprintf("    %s / %s: %s",
				runs[i].task, shortCodeArm(runs[i].arm), runs[i].note))
		}
	}
	if len(failures) > 0 {
		t.Log("")
		t.Log("what went wrong:")
		for _, f := range failures {
			t.Log(f)
		}
	}
}

func runsFor(runs []agentRun, arm string) []agentRun {
	var out []agentRun
	for i := range runs {
		if runs[i].arm == arm {
			out = append(out, runs[i])
		}
	}
	return out
}

func shortCodeArm(a string) string {
	switch {
	case strings.Contains(a, "sandbox"):
		return "perry+sandbox"
	case strings.HasPrefix(a, "perry"):
		return "perry (MCP)"
	case strings.Contains(a, "warm"):
		return "git warm"
	default:
		return "git cold"
	}
}

// TestCodeTaskGrading checks the grader before any money is spent on it.
//
// A grader that passes everything would report four perfect arms and mean
// nothing, and it would do so silently. So: for every task, the repository
// as the agent first sees it must be graded a failure. Nothing has been
// fixed at that point, and anything that calls it finished is broken.
//
// This needs no model and no API key, so it runs with the ordinary
// benchmark gate rather than the agent one.
func TestCodeTaskGrading(t *testing.T) {
	requireBench(t)

	for _, task := range codeTasks() {
		t.Run(task.name, func(t *testing.T) {
			// buildCalcCorpus already fails the test if the seeded fixture
			// still passes `go test`, which is the other half of this guard.
			c, baseTests := buildCalcCorpus(t, task)
			check := codeTaskCheck(t, baseTests)

			r := &perryReader{c: c, branch: "main"}
			if err := check(r); err == nil {
				t.Fatal("the grader passed the untouched repository, so it would pass an arm that did nothing")
			}

			// And it must notice a test file being edited out of the way,
			// which is the cheapest way to make a failing suite pass.
			if len(baseTests) == 0 {
				t.Fatal("no test files were captured, so the tamper check proves nothing")
			}
			var one string
			for p := range baseTests {
				if one == "" || p < one {
					one = p
				}
			}
			tampered := map[string]string{}
			for p, body := range baseTests {
				tampered[p] = body
			}
			tampered[one] = baseTests[one] + "// not what the agent was given"
			if err := codeTaskCheck(t, tampered)(r); err == nil {
				t.Fatal("the grader accepted a repository whose tests do not match the ones posed")
			}
		})
	}
}
