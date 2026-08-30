package githubactions

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

func TestParseNoContainerErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for job without container:, got nil")
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

func TestParseMissingRunOrUsesErrors(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    steps:
      - name: oops
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for step missing run: or uses:, got nil")
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
