package githubactions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

func TestParseSimple(t *testing.T) {
	data, err := os.ReadFile("testdata/simple.yml")
	if err != nil {
		t.Fatal(err)
	}

	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantStages := []string{"level-0", "level-1"}
	if len(p.Stages) != len(wantStages) {
		t.Fatalf("Stages = %v, want %v", p.Stages, wantStages)
	}
	for i, s := range wantStages {
		if p.Stages[i] != s {
			t.Errorf("Stages[%d] = %q, want %q", i, p.Stages[i], s)
		}
	}

	if len(p.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2: %+v", len(p.Jobs), p.Jobs)
	}

	build := p.Jobs[0]
	if build.Name != "build" || build.Stage != "level-0" || build.Image != "alpine:3.19" {
		t.Errorf("build job = %+v", build)
	}
	if build.Variables["GLOBAL_VAR"] != "hello" || build.Variables["JOB_VAR"] != "world" {
		t.Errorf("build job variables = %+v", build.Variables)
	}
	// uses: checkout, run: build, run: with step env
	if len(build.Steps) != 3 {
		t.Fatalf("build job steps = %+v, want 3", build.Steps)
	}
	if build.Steps[0].Name != "uses[0]" {
		t.Errorf("Steps[0].Name = %q, want uses[0]", build.Steps[0].Name)
	}
	if build.Steps[1].Name != "Build" || build.Steps[1].Command != "echo building" {
		t.Errorf("Steps[1] = %+v", build.Steps[1])
	}
	if build.Steps[2].Env["GREETING"] != "hi-from-step" {
		t.Errorf("Steps[2].Env = %+v", build.Steps[2].Env)
	}

	test := p.Jobs[1]
	if test.Name != "test" || test.Stage != "level-1" {
		t.Errorf("test job = %+v", test)
	}
}

// TestParseNoContainerIsSkippedNotFatal proves a job with no container: is
// individually skipped — recorded in SkippedJobs with a clear reason —
// rather than failing the whole file to parse. It's the only job here, so
// Parse still succeeds with zero runnable jobs (nothing else to run) — see
// TestParsePartialExecutionSkipsOnlyUnsupportedJob for the case where
// other jobs in the same file are unaffected.
func TestParseNoContainerIsSkippedNotFatal(t *testing.T) {
	data := []byte(`
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with build recorded as skipped", err)
	}
	if len(p.Jobs) != 0 {
		t.Errorf("Jobs = %+v, want none (the only job in the file is unsupported)", p.Jobs)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "build" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for build", p.SkippedJobs)
	}
	if p.SkippedJobs[0].Reason == "" {
		t.Error("SkippedJobs[0].Reason is empty, want an explanation")
	}
}

// TestParsePartialExecutionSkipsOnlyUnsupportedJob proves the core partial-
// execution behavior: a file with three runnable jobs and one job that
// can't run at all (no container:) parses successfully with the three
// runnable jobs intact and the fourth clearly recorded as skipped — not the
// whole file failing to parse over one bad job. A fifth job that needs: the
// unsupported one is also skipped (cascade), since it can't run without it,
// while the three fully-independent jobs are untouched.
func TestParsePartialExecutionSkipsOnlyUnsupportedJob(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps: [{run: echo build}]
  test:
    container: alpine:3.19
    steps: [{run: echo test}]
  lint:
    container: alpine:3.19
    steps: [{run: echo lint}]
  legacy-deploy:
    runs-on: ubuntu-latest
    steps: [{run: echo deploy}]
  publish:
    container: alpine:3.19
    needs: legacy-deploy
    steps: [{run: echo publish}]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with legacy-deploy/publish recorded as skipped", err)
	}

	gotJobs := map[string]bool{}
	for _, j := range p.Jobs {
		gotJobs[j.Name] = true
	}
	if len(p.Jobs) != 3 || !gotJobs["build"] || !gotJobs["test"] || !gotJobs["lint"] {
		t.Fatalf("Jobs = %+v, want exactly build, test, lint", p.Jobs)
	}

	gotSkipped := map[string]string{}
	for _, sj := range p.SkippedJobs {
		gotSkipped[sj.Name] = sj.Reason
	}
	if len(p.SkippedJobs) != 2 {
		t.Fatalf("SkippedJobs = %+v, want exactly 2 (legacy-deploy, publish)", p.SkippedJobs)
	}
	if gotSkipped["legacy-deploy"] == "" {
		t.Errorf("legacy-deploy should be skipped with a reason, got %+v", p.SkippedJobs)
	}
	if gotSkipped["publish"] == "" {
		t.Errorf("publish should be skipped (it needs: the unsupported legacy-deploy), got %+v", p.SkippedJobs)
	}
}

func TestParseUnknownNeedsErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - run: echo hi
    needs: deploy
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for needs referencing unknown job, got nil")
	}
}

func TestParseCircularNeedsErrors(t *testing.T) {
	data := []byte(`
jobs:
  a:
    container: alpine:3.19
    needs: b
    steps: [{run: echo a}]
  b:
    container: alpine:3.19
    needs: a
    steps: [{run: echo b}]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for circular needs, got nil")
	}
}

// TestParseMissingRunOrUsesIsSkipped proves a job with a malformed step
// (neither run: nor uses:) is skipped like any other job-level problem,
// rather than failing the whole file.
func TestParseMissingRunOrUsesIsSkipped(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - name: oops
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with build recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "build" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for build", p.SkippedJobs)
	}
}

func TestParseContainerMapForm(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container:
      image: alpine:3.19
      env:
        FROM_CONTAINER: yes
    steps:
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Jobs[0].Image != "alpine:3.19" {
		t.Errorf("Image = %q, want alpine:3.19", p.Jobs[0].Image)
	}
	if p.Jobs[0].Variables["FROM_CONTAINER"] != "yes" {
		t.Errorf("Variables = %+v", p.Jobs[0].Variables)
	}
}

func TestParseNeedsListFanIn(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps: [{run: echo build}]
  lint:
    container: alpine:3.19
    steps: [{run: echo lint}]
  deploy:
    container: alpine:3.19
    needs: [build, lint]
    steps: [{run: echo deploy}]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byName := map[string]string{}
	for _, j := range p.Jobs {
		byName[j.Name] = j.Stage
	}
	if byName["build"] != "level-0" || byName["lint"] != "level-0" {
		t.Errorf("build/lint stages = %+v, want level-0", byName)
	}
	if byName["deploy"] != "level-1" {
		t.Errorf("deploy stage = %q, want level-1", byName["deploy"])
	}
}

// TestParseFindingsClassifyUsesAndExpressions proves the features this
// parser knowingly doesn't fully implement — third-party uses: actions and
// an unsupported ${{ }} expression — are recorded as Findings for `polyci
// check` rather than silently passed through or dropped. actions/checkout
// is Emulated (the workspace mount is a real substitute); everything else
// here is Unsupported. (matrix. and github.sha/ref expressions are
// genuinely evaluated now — see TestParseExpression* and
// TestParseMatrixExpansion* — so this test uses secrets.TOKEN, which stays
// unsupported, as its "unrecognized expression" example.)
func TestParseFindingsClassifyUsesAndExpressions(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: echo "token is ${{ secrets.TOKEN }}"
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var checkoutFinding, setupGoFinding, exprFinding *pipeline.Finding
	for i := range p.Findings {
		f := &p.Findings[i]
		switch {
		case f.Feature == "actions/checkout@v4":
			checkoutFinding = f
		case f.Feature == "actions/setup-go@v5":
			setupGoFinding = f
		case f.Feature == "${{ secrets.TOKEN }}":
			exprFinding = f
		}
	}

	if checkoutFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for actions/checkout@v4", p.Findings)
	}
	if checkoutFinding.Job != "build" || checkoutFinding.Level != pipeline.Emulated {
		t.Errorf("checkout finding = %+v, want Job=build Level=Emulated", checkoutFinding)
	}

	if setupGoFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for actions/setup-go@v5", p.Findings)
	}
	if setupGoFinding.Job != "build" || setupGoFinding.Level != pipeline.Unsupported {
		t.Errorf("setup-go finding = %+v, want Job=build Level=Unsupported", setupGoFinding)
	}

	if exprFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for the unsupported secrets.TOKEN expression", p.Findings)
	}
	if exprFinding.Job != "build" || exprFinding.Level != pipeline.Unsupported {
		t.Errorf("expression finding = %+v, want Job=build Level=Unsupported", exprFinding)
	}
	wantCommand := `echo "token is ${{ secrets.TOKEN }}"`
	if p.Jobs[0].Steps[2].Command != wantCommand {
		t.Errorf("unsupported expression's command = %q, want %q (left unexpanded)", p.Jobs[0].Steps[2].Command, wantCommand)
	}

	// The plain `run: echo hi` step (no expression) must not produce a finding.
	for _, f := range p.Findings {
		if f.Step == "run[3]" {
			t.Errorf("unexpected finding for a plain run: step: %+v", f)
		}
	}
}

// TestParseMatrixExpansionProducesOneJobPerCombination proves a job with a
// strategy.matrix is actually expanded into one job per combination — not
// left as a single job with an "unsupported" finding — and that each
// expanded job's steps have the matching matrix.<key> substitution baked
// in, not the same literal text repeated.
func TestParseMatrixExpansionProducesOneJobPerCombination(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    strategy:
      matrix:
        node: [18, 20]
    steps:
      - run: echo "node version is ${{ matrix.node }}"
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (one per matrix combination): %+v", len(p.Jobs), p.Jobs)
	}

	got := map[string]string{} // job name -> step command
	for _, j := range p.Jobs {
		if len(j.Steps) != 1 {
			t.Fatalf("job %q steps = %+v, want 1", j.Name, j.Steps)
		}
		got[j.Name] = j.Steps[0].Command
	}

	if got["build (node=18)"] != `echo "node version is 18"` {
		t.Errorf(`job "build (node=18)" command = %q, want %q`, got["build (node=18)"], `echo "node version is 18"`)
	}
	if got["build (node=20)"] != `echo "node version is 20"` {
		t.Errorf(`job "build (node=20)" command = %q, want %q`, got["build (node=20)"], `echo "node version is 20"`)
	}
}

// TestParseExpressionEnvSubstitution proves ${{ env.KEY }} substitutes
// from the job's own resolved env (workflow-level env: merged with the
// job's own env:), not left as literal unexpanded text.
func TestParseExpressionEnvSubstitution(t *testing.T) {
	data := []byte(`
env:
  GREETING: hello-from-workflow
jobs:
  build:
    container: alpine:3.19
    steps:
      - run: echo "${{ env.GREETING }}"
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := `echo "hello-from-workflow"`
	if got := p.Jobs[0].Steps[0].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// TestParseStepShellAndWorkingDirectory proves a run: step's shell: and
// working-directory: keys are read into pipeline.Step's Shell and
// WorkingDirectory fields, and that a step which sets neither leaves them
// at the zero value (the executor's cue to fall back to its defaults).
func TestParseStepShellAndWorkingDirectory(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - run: echo hi
        shell: bash
        working-directory: subdir
      - run: echo plain
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	steps := p.Jobs[0].Steps
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
	}
	if steps[0].Shell != "bash" || steps[0].WorkingDirectory != "subdir" {
		t.Errorf("steps[0] = %+v, want Shell=bash WorkingDirectory=subdir", steps[0])
	}
	if steps[1].Shell != "" || steps[1].WorkingDirectory != "" {
		t.Errorf("steps[1] = %+v, want Shell and WorkingDirectory left empty (no shell:/working-directory: set)", steps[1])
	}
}

// initGitRepo creates a throwaway git repository at a fixed branch name
// (so the test doesn't depend on the environment's init.defaultBranch),
// with one commit, and returns its directory and HEAD's commit sha.
func initGitRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping test that needs a real repository")
	}
	dir = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "polyci-test")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

// TestParseExpressionGithubShaAndRef proves ${{ github.sha }} and
// ${{ github.ref }} substitute from the real local git repository at the
// workspace root — the same directory the executor bind-mounts at
// /workspace — rather than being left unevaluated.
func TestParseExpressionGithubShaAndRef(t *testing.T) {
	dir, sha := initGitRepo(t)

	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - run: echo "sha=${{ github.sha }} ref=${{ github.ref }}"
`)
	p, err := parseInDir(data, dir)
	if err != nil {
		t.Fatalf("parseInDir: %v", err)
	}
	want := fmt.Sprintf("echo \"sha=%s ref=refs/heads/polyci-test\"", sha)
	if got := p.Jobs[0].Steps[0].Command; got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
	// A successful substitution still records a Supported-level Finding
	// (so `polyci check`'s category breakdown has positive evidence
	// expressions were used and resolved cleanly), but never an
	// Emulated/Unsupported one — that would misreport a working expression
	// as a problem.
	var shaFinding, refFinding *pipeline.Finding
	for i := range p.Findings {
		f := &p.Findings[i]
		if strings.Contains(f.Feature, "github.sha") {
			shaFinding = f
		}
		if strings.Contains(f.Feature, "github.ref") {
			refFinding = f
		}
	}
	if shaFinding == nil || shaFinding.Level != pipeline.Supported {
		t.Errorf("github.sha finding = %+v, want a Supported-level finding", shaFinding)
	}
	if refFinding == nil || refFinding.Level != pipeline.Supported {
		t.Errorf("github.ref finding = %+v, want a Supported-level finding", refFinding)
	}
}

// TestParseExpressionGithubShaFallsBackToEmptyOutsideGitRepo proves that
// when the workspace isn't a git repository at all, github.sha/github.ref
// fall back to an empty string (rather than erroring or leaving the
// literal "${{ github.sha }}" text for the shell to choke on) and that
// fallback is surfaced as a Finding.
func TestParseExpressionGithubShaFallsBackToEmptyOutsideGitRepo(t *testing.T) {
	dir := t.TempDir() // deliberately not a git repository

	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - run: echo "sha=[${{ github.sha }}]"
`)
	p, err := parseInDir(data, dir)
	if err != nil {
		t.Fatalf("parseInDir: %v", err)
	}
	want := `echo "sha=[]"`
	if got := p.Jobs[0].Steps[0].Command; got != want {
		t.Errorf("Command = %q, want %q (empty-string fallback)", got, want)
	}

	found := false
	for _, f := range p.Findings {
		if f.Feature == "${{ github.sha }}" && f.Level == pipeline.Unsupported {
			found = true
		}
	}
	if !found {
		t.Errorf("Findings = %+v, want a finding warning github.sha fell back to empty (no git repository)", p.Findings)
	}
}
