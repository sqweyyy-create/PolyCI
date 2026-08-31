package githubactions

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

// TestParseFindingsClassifyUsesExpressionsAndMatrix proves the features
// this parser knowingly doesn't fully implement — third-party uses:
// actions, unevaluated ${{ }} expressions, and strategy.matrix: — are
// recorded as Findings for `polyci check` rather than silently passed
// through or dropped. actions/checkout is Emulated (the workspace mount is
// a real substitute); everything else here is Unsupported.
func TestParseFindingsClassifyUsesExpressionsAndMatrix(t *testing.T) {
	data := []byte(`
jobs:
  build:
    container: alpine:3.19
    strategy:
      matrix:
        version: [1, 2]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: echo "building ${{ github.sha }}"
      - run: echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var checkoutFinding, setupGoFinding, exprFinding, matrixFinding *pipeline.Finding
	for i := range p.Findings {
		f := &p.Findings[i]
		switch {
		case f.Feature == "actions/checkout@v4":
			checkoutFinding = f
		case f.Feature == "actions/setup-go@v5":
			setupGoFinding = f
		case f.Feature == "${{ }}":
			exprFinding = f
		case f.Feature == "strategy.matrix:":
			matrixFinding = f
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
		t.Fatalf("Findings = %+v, want a finding for the ${{ }} expression", p.Findings)
	}
	if exprFinding.Job != "build" || exprFinding.Level != pipeline.Unsupported {
		t.Errorf("expression finding = %+v, want Job=build Level=Unsupported", exprFinding)
	}

	if matrixFinding == nil {
		t.Fatalf("Findings = %+v, want a finding for strategy.matrix:", p.Findings)
	}
	if matrixFinding.Job != "build" || matrixFinding.Level != pipeline.Unsupported {
		t.Errorf("matrix finding = %+v, want Job=build Level=Unsupported", matrixFinding)
	}

	// The plain `run: echo hi` step (no expression) must not produce a finding.
	for _, f := range p.Findings {
		if f.Step == "run[3]" {
			t.Errorf("unexpected finding for a plain run: step: %+v", f)
		}
	}
}
