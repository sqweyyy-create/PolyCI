package circleci

import (
	"os"
	"testing"
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

func TestParseUnknownWorkflowJobErrors(t *testing.T) {
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
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for job not defined under jobs:, got nil")
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

func TestParseNonDockerExecutorErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    machine: true
    steps:
      - run: echo hi
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for non-docker executor, got nil")
	}
}

func TestParseMissingRunCommandErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps:
      - run:
          name: oops
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for run step missing command, got nil")
	}
}

func TestParseUnsupportedStepTypeErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    docker: [{image: alpine:3.19}]
    steps:
      - some_orb/some_command
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for unsupported step type, got nil")
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
