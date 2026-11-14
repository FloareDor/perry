package bench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/object"
)

// A suite of agent tasks, where realagent_test.go runs one.
//
// One task at three trials established that the ordering can go perry's way.
// It did not establish that it generally does, and the gap between those two
// claims is the whole question. The single task was an append to the end of
// one file, which is the easiest edit there is: no anchor to find, no
// ambiguity, nothing to understand first.
//
// This file poses ten task shapes instead -- an edit in the middle of a file,
// two files at once, a comprehension step before the edit, two commits in
// sequence, a file that has to be created, a count the agent has to get right
// -- across both corpora, and reports completion rate alongside latency and
// cost.
//
// Completion is the headline, not latency. A tool surface that is quick and
// gets the task wrong is worth less than a slow one that does not, and a few
// seconds of wall clock is noise next to a task the agent failed to finish.

// job is one task posed to one arm.
//
// The goal text is shared verbatim across arms. Only the preamble differs,
// because one arm has a shell and a checkout and the other has seven tools;
// any other difference in the wording would be measuring the prompt rather
// than the tools. That is not hypothetical: an earlier version of the
// single-task benchmark told perry's arm to read the file and did not tell
// git's, and the asymmetry went unnoticed through two full runs.
type job struct {
	task   string
	tag    string
	goal   string
	trial  int
	rtt    time.Duration
	verify func(r repoReader) error
}

// repoReader reads the finished repository the way verification needs to,
// without caring which arm produced it. The perry arm is read out of the
// object store and the git arms out of the bare origin, so neither is
// checked through the path it wrote.
type repoReader interface {
	// grepFiles returns the paths whose contents contain a literal.
	grepFiles(literal string) ([]string, error)
	read(path string) (string, error)
	// newCommits counts the commits added since the agent started.
	newCommits() (int, error)
}

type perryReader struct {
	c      *corpus
	branch string
	base   string
}

func (p *perryReader) grepFiles(literal string) ([]string, error) {
	res, err := api.Grep(context.Background(), p.c.store, api.GrepOptions{
		Pattern: regexp.QuoteMeta(literal), Rev: p.branch, Limit: 200,
	})
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range res.Matches {
		if !seen[m.Path] {
			seen[m.Path] = true
			out = append(out, m.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *perryReader) read(path string) (string, error) {
	b, err := api.ReadFile(context.Background(), p.c.store, p.branch, path)
	return string(b), err
}

func (p *perryReader) newCommits() (int, error) {
	ctx := context.Background()
	tip, err := api.Resolve(ctx, p.c.store, p.branch)
	if err != nil {
		return 0, err
	}
	n := 0
	for cur := tip.String(); cur != p.base && n < 100; n++ {
		obj, err := p.c.store.Get(ctx, mustID(cur))
		if err != nil {
			return n, err
		}
		commit, err := decodeCommitPayload(obj)
		if err != nil {
			return n, err
		}
		if len(commit) == 0 {
			break
		}
		cur = commit[0]
	}
	return n, nil
}

type gitReader struct {
	dir  string
	ref  string
	base string
}

func (g *gitReader) grepFiles(literal string) ([]string, error) {
	// A non-zero exit means no match, which is an answer rather than a
	// failure, so the error is swallowed and the empty output speaks.
	out, _ := gitQuiet(g.dir, "grep", "-l", "-F", literal, g.ref)
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// git grep prefixes each path with "<ref>:".
		paths = append(paths, strings.TrimPrefix(line, g.ref+":"))
	}
	sort.Strings(paths)
	return paths, nil
}

func (g *gitReader) read(path string) (string, error) {
	out, err := gitQuiet(g.dir, "show", g.ref+":"+path)
	if err != nil {
		return "", fmt.Errorf("%s is not in %s", path, g.ref)
	}
	return out, nil
}

func (g *gitReader) newCommits() (int, error) {
	out, err := gitQuiet(g.dir, "rev-list", "--count", g.base+".."+g.ref)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// markerLanded is the default check: the marker is somewhere in the
// published repository. It is what the single-task comparison uses.
func markerLanded(marker string) func(repoReader) error {
	want := strings.TrimSpace(marker)
	return func(r repoReader) error {
		files, err := r.grepFiles(want)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("the marker is not in the published repository")
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// The tasks.
//
// Each one is posed as plain instructions and checked by reading the result,
// never by believing the model's report. Several deliberately have a right
// answer the harness computes independently -- a package name, a file's
// length, how many files match -- so that "the marker is present" cannot pass
// a task the agent only half did.
// ---------------------------------------------------------------------------

type evalTask struct {
	name string
	what string
	// build returns the shared goal text and the check for it. It gets the
	// corpus so a task can compute the right answer from the repository
	// before the agent ever sees it.
	build func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error)
}

func evalTasks() []evalTask {
	return []evalTask{
		{
			name: "append-end",
			what: "append a line to the end of a located file",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Append this exact line to the very end of it, after the last line: %s

Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, marker)

				return goal, func(r repoReader) error {
					path, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					if last := lastNonEmptyLine(body); !strings.Contains(last, marker) {
						return fmt.Errorf("%s does not end with the line; its last line is %q", path, trunc(last))
					}
					if !strings.Contains(body, c.spec.needle) {
						return fmt.Errorf("%s no longer contains %q", path, c.spec.needle)
					}
					return nil
				}
			},
		},
		{
			name: "insert-above",
			what: "insert a line immediately above a located line",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Insert this exact line immediately above the first line that contains %q: %s

There must be nothing between the inserted line and that line.
Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, c.spec.needle, marker)

				return goal, func(r repoReader) error {
					path, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					lines := strings.Split(body, "\n")
					for i, l := range lines {
						if !strings.Contains(l, marker) {
							continue
						}
						if i+1 >= len(lines) {
							return fmt.Errorf("%s: the line was added at the very end, not above the match", path)
						}
						if !strings.Contains(lines[i+1], c.spec.needle) {
							return fmt.Errorf("%s: the line below it is %q, not the one containing %q",
								path, trunc(lines[i+1]), c.spec.needle)
						}
						return nil
					}
					return fmt.Errorf("%s does not contain the line", path)
				}
			},
		},
		{
			name: "two-files",
			what: "edit a located file and a named one in the same commit",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Append this exact line to the very end of it: %s
3. Also append this exact line to the very end of the file go.mod at the repository root: // %s

Keep the rest of both files byte-for-byte unchanged.`, c.spec.needle, marker, marker)

				return goal, func(r repoReader) error {
					files, err := r.grepFiles(marker)
					if err != nil {
						return err
					}
					if len(files) < 2 {
						return fmt.Errorf("the line reached %d file(s), not both: %v", len(files), files)
					}
					mod, err := r.read("go.mod")
					if err != nil {
						return err
					}
					if !strings.Contains(lastNonEmptyLine(mod), marker) {
						return fmt.Errorf("go.mod does not end with the line")
					}
					return nil
				}
			},
		},
		{
			name: "read-then-answer",
			what: "report the package a located file declares",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				// The harness works out the right answer first, so the
				// check cannot be satisfied by a plausible guess.
				path := findMarkedPath(t, c, "main", c.spec.needle)
				body, err := api.ReadFile(context.Background(), c.store, "main", path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}
				pkg := packageClause(string(body))
				if pkg == "" {
					t.Fatalf("%s has no package clause", path)
				}

				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Read its package declaration.
3. Append this exact line to the very end of that file, filling in the package name: %s package=<name>

Replace <name> with the package the file declares, and nothing else.
Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, marker)

				want := marker + " package=" + pkg
				return goal, func(r repoReader) error {
					_, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					if !strings.Contains(body, want) {
						return fmt.Errorf("expected a line reading %q; the file has %q",
							want, trunc(lastNonEmptyLine(body)))
					}
					return nil
				}
			},
		},
		{
			name: "count-matches",
			what: "report how many files contain a string",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				res, err := api.Grep(context.Background(), c.store, api.GrepOptions{
					Pattern: regexp.QuoteMeta(c.spec.needle), Rev: "main", Limit: 500,
				})
				if err != nil {
					t.Fatalf("counting %q: %v", c.spec.needle, err)
				}
				want := fmt.Sprintf("%s files=%d", marker, res.Matched)

				goal := fmt.Sprintf(`1. Count how many files in the repository contain the string %q.
2. Find the first of those files.
3. Append this exact line to the very end of it, filling in the count: %s files=<n>

Replace <n> with the number of files that contain the string, as a plain integer.
Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, marker)

				return goal, func(r repoReader) error {
					_, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					if !strings.Contains(body, want) {
						return fmt.Errorf("expected %q; the file has %q",
							want, trunc(lastNonEmptyLine(body)))
					}
					return nil
				}
			},
		},
		{
			name: "length-branch",
			what: "choose between two answers by reading the file",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				path := findMarkedPath(t, c, "main", c.spec.needle)
				body, err := api.ReadFile(context.Background(), c.store, "main", path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}
				long := strings.Count(string(body), "\n") >= 200
				want := marker + " SHORT"
				if long {
					want = marker + " LONG"
				}

				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Count its lines.
3. If it has 200 lines or more, append this exact line to the very end of it: %s LONG
   Otherwise append this exact line instead: %s SHORT

Append exactly one of the two.
Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, marker, marker)

				return goal, func(r repoReader) error {
					_, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					if !strings.Contains(body, want) {
						return fmt.Errorf("expected %q; the file has %q",
							want, trunc(lastNonEmptyLine(body)))
					}
					return nil
				}
			},
		},
		{
			name: "two-commits",
			what: "two commits in sequence, second on top of the first",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Append this exact line to the very end of it and commit that: %s one
3. Then append this exact line to the very end of the same file and commit that
   separately, as a second commit on top of the first: %s two

The result must be two commits, not one.
Keep the rest of the file byte-for-byte unchanged.`, c.spec.needle, marker, marker)

				return goal, func(r repoReader) error {
					_, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					for _, want := range []string{marker + " one", marker + " two"} {
						if !strings.Contains(body, want) {
							return fmt.Errorf("the file is missing %q", want)
						}
					}
					if i, j := strings.Index(body, marker+" one"), strings.Index(body, marker+" two"); i > j {
						return fmt.Errorf("the two lines are in the wrong order")
					}
					n, err := r.newCommits()
					if err != nil {
						return err
					}
					if n < 2 {
						return fmt.Errorf("the work landed in %d commit(s), not two", n)
					}
					return nil
				}
			},
		},
		{
			name: "new-file",
			what: "create a file that does not exist, in a new directory",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`Create a new file at the path notes/agent.md. It must not exist already;
if it does, overwrite it. Its entire contents must be exactly these two lines:

# Agent note
%s

Do not change any other file.`, marker)

				return goal, func(r repoReader) error {
					body, err := r.read("notes/agent.md")
					if err != nil {
						return err
					}
					if !strings.Contains(body, "# Agent note") || !strings.Contains(body, marker) {
						return fmt.Errorf("notes/agent.md has unexpected contents: %q", trunc(body))
					}
					return nil
				}
			},
		},
		{
			name: "replace-all",
			what: "replace every occurrence of a string in one file",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				path := findMarkedPath(t, c, "main", c.spec.needle)
				body, err := api.ReadFile(context.Background(), c.store, "main", path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}
				want := strings.Count(string(body), c.spec.needle)
				renamed := c.spec.needle + "Renamed"

				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. In that file only, rename every occurrence of %q to %q. There are several;
   all of them must be renamed.
3. Append this exact line to the very end of the same file: %s

Change no other file.`, c.spec.needle, c.spec.needle, renamed, marker)

				return goal, func(r repoReader) error {
					path, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					if got := strings.Count(body, renamed); got != want {
						return fmt.Errorf("%s has %d occurrences of %s, want %d",
							path, got, renamed, want)
					}
					// Every bare occurrence must be gone, which is what
					// "all of them" means and what a one-at-a-time edit
					// would leave behind.
					if bare := strings.Count(body, c.spec.needle) - strings.Count(body, renamed); bare != 0 {
						return fmt.Errorf("%s still has %d un-renamed occurrence(s)", path, bare)
					}
					return nil
				}
			},
		},
		{
			name: "top-and-bottom",
			what: "two edits to one file, at both ends",
			build: func(t *testing.T, c *corpus, marker string) (string, func(repoReader) error) {
				goal := fmt.Sprintf(`1. Find the file that contains the string %q.
2. Insert this exact line as the very first line of that file, above everything: %s top
3. Append this exact line as the very last line of the same file: %s bottom

Both must be in the same file. Keep everything between them byte-for-byte unchanged.`,
					c.spec.needle, marker, marker)

				return goal, func(r repoReader) error {
					path, body, err := theMarkedFile(r, marker)
					if err != nil {
						return err
					}
					first := strings.SplitN(body, "\n", 2)[0]
					if !strings.Contains(first, marker+" top") {
						return fmt.Errorf("%s starts with %q", path, trunc(first))
					}
					if last := lastNonEmptyLine(body); !strings.Contains(last, marker+" bottom") {
						return fmt.Errorf("%s ends with %q", path, trunc(last))
					}
					return nil
				}
			},
		},
	}
}

// theMarkedFile returns the one file that carries the marker, and fails
// clearly when the agent scattered it or missed entirely.
func theMarkedFile(r repoReader, marker string) (string, string, error) {
	files, err := r.grepFiles(marker)
	if err != nil {
		return "", "", err
	}
	switch len(files) {
	case 0:
		return "", "", fmt.Errorf("the marker is not in the published repository")
	case 1:
		body, err := r.read(files[0])
		return files[0], body, err
	default:
		return "", "", fmt.Errorf("the marker landed in %d files: %v", len(files), files)
	}
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

func packageClause(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(l), "package "); ok {
			return strings.Fields(after)[0]
		}
	}
	return ""
}

func trunc(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 70 {
		return s[:70] + "..."
	}
	return s
}

func mustID(s string) object.ID {
	id, err := object.ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// decodeCommitPayload returns a commit's parent ids as strings.
func decodeCommitPayload(obj *object.Object) ([]string, error) {
	c, err := object.DecodeCommit(obj.Payload)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(c.Parents))
	for _, p := range c.Parents {
		out = append(out, p.String())
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The suite.
// ---------------------------------------------------------------------------

func TestAgentTaskSuite(t *testing.T) {
	bin := requireAgentBench(t)
	if os.Getenv("PERRY_EVAL") == "" {
		t.Skip("the multi-task agent suite is opt in, and costs more than the single-task one. Run it with:\n" +
			"\tPERRY_BENCH=1 PERRY_AGENT_BENCH=1 PERRY_EVAL=1 go test ./internal/bench -run AgentTaskSuite -v -timeout 180m")
	}

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

	// Latency is held at zero here. The single-task comparison already
	// measured what latency does, and varying it would multiply the cost of
	// this suite for an answer it is not asking.
	rtt := time.Duration(0)
	tasks := selectedTasks(t)

	var runs []agentRun
	for _, spec := range evalRepos(t) {
		c := buildRealCorpus(t, spec)
		gitRoot := t.TempDir()
		gitSrv := gitHTTPBackend(t, gitRoot)
		gitNet := newNetShaper(t, gitSrv.URL, rtt)
		perryNet := newNetShaper(t, c.ts.URL, rtt)

		for _, task := range tasks {
			marker := fmt.Sprintf("// perry-eval-%s-%d", task.name, time.Now().UnixNano())
			goal, check := task.build(t, c, marker)
			j := job{
				task:   spec.label + "/" + task.name,
				tag:    fmt.Sprintf("%s-%s", spec.label, task.name),
				goal:   goal,
				trial:  1,
				rtt:    rtt,
				verify: check,
			}
			t.Logf("  %s: %s", j.task, task.what)

			runs = append(runs,
				perryArm(t, bin, c, perryBin, perryNet, j, results),
				gitArm(t, bin, c, gitRoot, gitNet, j, results,
					"git shallow", "shallow", false,
					"--depth", "1"),
				gitArm(t, bin, c, gitRoot, gitNet, j, results,
					"git sparse (told the dir)", "sparse", true,
					"--depth", "1", "--filter=blob:none", "--sparse"),
			)
		}
	}

	reportTaskSuite(t, runs)

	landed := 0
	for _, r := range runs {
		if r.landed {
			landed++
		}
	}
	if landed == 0 {
		t.Fatalf("no arm completed any of %d runs; the harness measured nothing", len(runs))
	}
}

// selectedTasks lets a smoke run take one task without editing the table.
func selectedTasks(t *testing.T) []evalTask {
	all := evalTasks()
	want := os.Getenv("PERRY_EVAL_TASKS")
	if want == "" {
		return all
	}
	keep := map[string]bool{}
	for _, n := range strings.Split(want, ",") {
		keep[strings.TrimSpace(n)] = true
	}
	var out []evalTask
	for _, task := range all {
		if keep[task.name] {
			out = append(out, task)
		}
	}
	if len(out) == 0 {
		t.Fatalf("PERRY_EVAL_TASKS=%q matched none of the %d tasks", want, len(all))
	}
	return out
}

func evalRepos(t *testing.T) []repoSpec {
	want := os.Getenv("PERRY_EVAL_CORPORA")
	if want == "" {
		return realRepos
	}
	var out []repoSpec
	for _, spec := range realRepos {
		if strings.Contains(want, spec.label) {
			out = append(out, spec)
		}
	}
	if len(out) == 0 {
		t.Fatalf("PERRY_EVAL_CORPORA=%q matched no corpus", want)
	}
	return out
}

// reportTaskSuite prints completion first, because that is the question.
func reportTaskSuite(t *testing.T, runs []agentRun) {
	t.Log("")
	t.Logf("multi-task agent suite: %s, one trial per task", agentModel)
	t.Log("")

	arms := armOrder(runs)

	t.Log("per task, did the arm finish it:")
	header := fmt.Sprintf("%-28s", "task")
	for _, a := range arms {
		header += fmt.Sprintf(" %-14s", shortArm(a))
	}
	t.Log(header)

	for _, task := range taskOrder(runs) {
		line := fmt.Sprintf("%-28s", task)
		for _, a := range arms {
			r := findRun(runs, task, a)
			switch {
			case r == nil:
				line += fmt.Sprintf(" %-14s", "-")
			case r.landed:
				line += fmt.Sprintf(" %-14s", fmt.Sprintf("yes %5.1fs", r.total.Seconds()))
			default:
				line += fmt.Sprintf(" %-14s", "NO")
			}
		}
		t.Log(line)
	}

	t.Log("")
	t.Log("totals:")
	t.Logf("%-28s %8s %10s %10s %10s %12s", "arm", "done", "median", "cost/task", "out tok", "wire/task")
	for _, a := range arms {
		var done int
		var landedRuns []agentRun
		var cost float64
		var out, wire int64
		var n int
		for i := range runs {
			if runs[i].arm != a {
				continue
			}
			n++
			cost += runs[i].costUSD
			out += int64(runs[i].outTok)
			wire += runs[i].wire
			if runs[i].landed {
				done++
				landedRuns = append(landedRuns, runs[i])
			}
		}
		if n == 0 {
			continue
		}
		t.Logf("%-28s %4d/%-3d %9s %10.4f %10d %12s",
			a, done, n, medianDur(landedRuns, func(r agentRun) time.Duration { return r.total }).Round(time.Millisecond),
			cost/float64(n), out/int64(n), humanBytes(wire/int64(n)))
	}

	var failures []string
	for i := range runs {
		if !runs[i].landed {
			failures = append(failures, fmt.Sprintf("    %s / %s: %s",
				runs[i].task, shortArm(runs[i].arm), runs[i].note))
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

func armOrder(runs []agentRun) []string {
	var arms []string
	seen := map[string]bool{}
	for i := range runs {
		if !seen[runs[i].arm] {
			seen[runs[i].arm] = true
			arms = append(arms, runs[i].arm)
		}
	}
	return arms
}

func taskOrder(runs []agentRun) []string {
	var tasks []string
	seen := map[string]bool{}
	for i := range runs {
		if !seen[runs[i].task] {
			seen[runs[i].task] = true
			tasks = append(tasks, runs[i].task)
		}
	}
	return tasks
}

func findRun(runs []agentRun, task, arm string) *agentRun {
	for i := range runs {
		if runs[i].task == task && runs[i].arm == arm {
			return &runs[i]
		}
	}
	return nil
}

func shortArm(a string) string {
	switch {
	case strings.HasPrefix(a, "perry"):
		return "perry"
	case strings.Contains(a, "sparse"):
		return "git sparse"
	default:
		return "git shallow"
	}
}
