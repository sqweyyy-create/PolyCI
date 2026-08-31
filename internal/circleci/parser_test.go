package circleci

import (
	"os"
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

	wantStages := []string{"build-and-test/level-0", "build-and-test/level-1"}
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
	if build.Name != "build" || build.Stage != "build-and-test/level-0" || build.Image != "cimg/base:2023.03" {
		t.Errorf("build job = %+v", build)
	}
	if build.Variables["GLOBAL_VAR"] != "hello" {
		t.Errorf("build job variables = %+v", build.Variables)
	}
	// checkout, run: echo building, run: (named, with step env)
	if len(build.Steps) != 3 {
		t.Fatalf("build job steps = %+v, want 3", build.Steps)
	}
	if build.Steps[0].Name != "checkout[0]" {
		t.Errorf("Steps[0].Name = %q, want checkout[0]", build.Steps[0].Name)
	}
	if build.Steps[1].Command != "echo building" {
		t.Errorf("Steps[1].Command = %q", build.Steps[1].Command)
	}
	if build.Steps[2].Name != "Run with custom env" {
		t.Errorf("Steps[2].Name = %q", build.Steps[2].Name)
	}
	if build.Steps[2].Env["GREETING"] != "hi-from-step" {
		t.Errorf("Steps[2].Env = %+v", build.Steps[2].Env)
	}

	test := p.Jobs[1]
	if test.Name != "test" || test.Stage != "build-and-test/level-1" {
		t.Errorf("test job = %+v", test)
	}
}

func TestParseNoWorkflowsDefaultsToBuildJob(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker:
      - image: alpine:3.19
    steps:
      - run: echo hi
  extra:
    docker:
      - image: alpine:3.19
    steps:
      - run: echo unused
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 1 || p.Jobs[0].Name != "build" {
		t.Fatalf("Jobs = %+v, want only 'build'", p.Jobs)
	}
}

func TestParseNoWorkflowsNoBuildJobErrors(t *testing.T) {
	data := []byte(`
jobs:
  compile:
    docker:
      - image: alpine:3.19
    steps:
      - run: echo hi
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error when no workflows and no 'build' job, got nil")
	}
}

// TestParseUnknownWorkflowJobIsSkipped proves a workflow job reference with
// no matching top-level jobs: definition (most commonly an orb-provided
// job, which this parser doesn't expand) is individually skipped — build
// still runs normally — rather than failing the whole file to parse. See
// COMPATIBILITY.md's circleci-demo-python-flask finding, which is exactly
// this shape (one orb-executor job blocking an otherwise-plain-docker:
// file).
func TestParseUnknownWorkflowJobIsSkipped(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker:
      - image: alpine:3.19
    steps:
      - run: echo hi
workflows:
  main:
    jobs:
      - build
      - deploy
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with deploy recorded as skipped", err)
	}
	if len(p.Jobs) != 1 || p.Jobs[0].Name != "build" {
		t.Fatalf("Jobs = %+v, want only build", p.Jobs)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "deploy" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for deploy", p.SkippedJobs)
	}
}

func TestParseCircularRequiresErrors(t *testing.T) {
	data := []byte(`
jobs:
  a:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo a}]
  b:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo b}]
workflows:
  main:
    jobs:
      - a:
          requires: [b]
      - b:
          requires: [a]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for circular requires, got nil")
	}
}

// TestParseNonDockerExecutorIsSkipped proves a job requesting a
// non-docker executor is skipped, not a fatal parse error — it's the only
// job here, so Parse still succeeds with zero runnable jobs.
func TestParseNonDockerExecutorIsSkipped(t *testing.T) {
	data := []byte(`
jobs:
  build:
    machine: true
    steps:
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with build recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "build" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for build", p.SkippedJobs)
	}
}

func TestParseMissingRunCommandIsSkipped(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps:
      - run:
          name: oops
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with build recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "build" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for build", p.SkippedJobs)
	}
}

func TestParseUnsupportedStepTypeIsSkipped(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps:
      - some_orb/some_command
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with build recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "build" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for build", p.SkippedJobs)
	}
}

// TestParsePartialExecutionSkipsOnlyUnsupportedJob proves the core
// partial-execution behavior for CircleCI: a workflow with three runnable
// jobs and one job that can't run at all (a non-docker executor) still
// runs the three, with the fourth clearly recorded as skipped rather than
// the whole file failing to parse. A fifth job that requires: the
// unsupported one is also skipped (cascade).
func TestParsePartialExecutionSkipsOnlyUnsupportedJob(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo build}]
  test:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo test}]
  lint:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo lint}]
  legacy-deploy:
    machine: true
    steps: [{run: echo deploy}]
  publish:
    docker: [{image: alpine:3.19}]
    steps: [{run: echo publish}]
workflows:
  main:
    jobs:
      - build
      - test
      - lint
      - legacy-deploy
      - publish:
          requires: [legacy-deploy]
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
		t.Errorf("publish should be skipped (it requires: the unsupported legacy-deploy), got %+v", p.SkippedJobs)
	}
}

func TestDependencyLevelsFanOutFanIn(t *testing.T) {
	// build -> {test, lint} -> deploy
	refs := []workflowJobRef{
		{name: "build"},
		{name: "test", requires: []string{"build"}},
		{name: "lint", requires: []string{"build"}},
		{name: "deploy", requires: []string{"test", "lint"}},
	}
	levels, order, err := dependencyLevels(refs)
	if err != nil {
		t.Fatalf("dependencyLevels: %v", err)
	}
	want := map[string]int{"build": 0, "test": 1, "lint": 1, "deploy": 2}
	for name, lvl := range want {
		if levels[name] != lvl {
			t.Errorf("levels[%q] = %d, want %d", name, levels[name], lvl)
		}
	}
	wantOrder := []string{"build", "test", "lint", "deploy"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Errorf("order[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestParseSecondaryDockerImagesBecomeServices(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker:
      - image: cimg/base:2023.03
      - image: postgres:15
      - image: redis:7
        name: cache
        environment:
          REDIS_PASSWORD: secret
    steps:
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Jobs[0].Image != "cimg/base:2023.03" {
		t.Errorf("Image = %q, want cimg/base:2023.03 (first docker: entry)", p.Jobs[0].Image)
	}
	services := p.Jobs[0].Services
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(services), services)
	}
	if services[0].Image != "postgres:15" || services[0].Alias != "postgres" {
		t.Errorf("services[0] = %+v, want image postgres:15 with default alias postgres", services[0])
	}
	if services[1].Image != "redis:7" || services[1].Alias != "cache" || services[1].Variables["REDIS_PASSWORD"] != "secret" {
		t.Errorf("services[1] = %+v", services[1])
	}
}

// TestParseFindingsClassifyNoOpSteps proves checkout and other unsupported
// builtin steps are recorded as Findings for `polyci check` — checkout as
// Emulated (the workspace mount is a real substitute), the rest as
// Unsupported (a pure no-op with no substitute) — not just silently
// turned into a no-op step with no trace of what was skipped.
func TestParseFindingsClassifyNoOpSteps(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps:
      - checkout
      - save_cache:
          key: v1
          paths: [~/cache]
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var checkoutFinding, saveCacheFinding *pipeline.Finding
	for i := range p.Findings {
		f := &p.Findings[i]
		switch f.Feature {
		case "checkout":
			checkoutFinding = f
		case "save_cache":
			saveCacheFinding = f
		}
	}

	if checkoutFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for checkout", p.Findings)
	}
	if checkoutFinding.Job != "build" || checkoutFinding.Level != pipeline.Emulated {
		t.Errorf("checkout finding = %+v, want Job=build Level=Emulated", checkoutFinding)
	}

	if saveCacheFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for save_cache", p.Findings)
	}
	if saveCacheFinding.Job != "build" || saveCacheFinding.Level != pipeline.Unsupported {
		t.Errorf("save_cache finding = %+v, want Job=build Level=Unsupported", saveCacheFinding)
	}

	// The run: step is fully supported and must not produce a finding.
	for _, f := range p.Findings {
		if f.Feature == "run" {
			t.Errorf("unexpected finding for a plain run: step: %+v", f)
		}
	}
}
