package gitlab

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

	wantStages := []string{"build", "test"}
	if len(p.Stages) != len(wantStages) {
		t.Fatalf("Stages = %v, want %v", p.Stages, wantStages)
	}
	for i, s := range wantStages {
		if p.Stages[i] != s {
			t.Errorf("Stages[%d] = %q, want %q", i, p.Stages[i], s)
		}
	}

	if len(p.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (hidden template must be excluded): %+v", len(p.Jobs), p.Jobs)
	}

	build := p.Jobs[0]
	if build.Name != "build-job" || build.Stage != "build" || build.Image != "alpine:3.19" {
		t.Errorf("build job = %+v", build)
	}
	if build.Variables["GLOBAL_VAR"] != "hello" || build.Variables["JOB_VAR"] != "world" {
		t.Errorf("build job variables = %+v", build.Variables)
	}
	if len(build.Steps) != 1 || build.Steps[0].Command != "echo building" {
		t.Errorf("build job steps = %+v", build.Steps)
	}

	test := p.Jobs[1]
	if test.Name != "test-job" || test.Stage != "test" {
		t.Errorf("test job = %+v", test)
	}
	wantSteps := []string{"echo setup", "echo testing", "echo cleanup"}
	if len(test.Steps) != len(wantSteps) {
		t.Fatalf("test job steps = %+v, want commands %v", test.Steps, wantSteps)
	}
	for i, cmd := range wantSteps {
		if test.Steps[i].Command != cmd {
			t.Errorf("test job Steps[%d].Command = %q, want %q", i, test.Steps[i].Command, cmd)
		}
	}
}

// TestParseMissingStageIsSkipped proves a job whose stage: isn't declared
// in stages: is skipped, not a fatal parse error for the whole file — it's
// the only job here, so Parse still succeeds with zero runnable jobs.
func TestParseMissingStageIsSkipped(t *testing.T) {
	data := []byte(`
stages: [build]
job1:
  stage: deploy
  image: alpine
  script: [echo hi]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with job1 recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "job1" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for job1", p.SkippedJobs)
	}
}

func TestParseMissingImageIsSkipped(t *testing.T) {
	data := []byte(`
job1:
  script: [echo hi]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with job1 recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "job1" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for job1", p.SkippedJobs)
	}
}

func TestParseMissingScriptIsSkipped(t *testing.T) {
	data := []byte(`
job1:
  image: alpine
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with job1 recorded as skipped", err)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "job1" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for job1", p.SkippedJobs)
	}
}

// TestParsePartialExecutionSkipsOnlyUnsupportedJob proves the core
// partial-execution behavior for GitLab: a file with three runnable jobs
// and one job that can't run at all (an undeclared stage) still runs the
// three, with the fourth clearly recorded as skipped rather than the whole
// file failing to parse.
func TestParsePartialExecutionSkipsOnlyUnsupportedJob(t *testing.T) {
	data := []byte(`
stages: [build, test]
build:
  stage: build
  image: alpine
  script: [echo build]
test:
  stage: test
  image: alpine
  script: [echo test]
lint:
  stage: test
  image: alpine
  script: [echo lint]
ghost:
  stage: nonexistent-stage
  image: alpine
  script: [echo ghost]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v, want success with ghost recorded as skipped", err)
	}

	gotJobs := map[string]bool{}
	for _, j := range p.Jobs {
		gotJobs[j.Name] = true
	}
	if len(p.Jobs) != 3 || !gotJobs["build"] || !gotJobs["test"] || !gotJobs["lint"] {
		t.Fatalf("Jobs = %+v, want exactly build, test, lint", p.Jobs)
	}
	if len(p.SkippedJobs) != 1 || p.SkippedJobs[0].Name != "ghost" {
		t.Fatalf("SkippedJobs = %+v, want a single entry for ghost", p.SkippedJobs)
	}
}

func TestParseDefaultImage(t *testing.T) {
	data := []byte(`
default:
  image: golang:1.22
job1:
  script: [go build ./...]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Jobs[0].Image != "golang:1.22" {
		t.Errorf("Image = %q, want golang:1.22", p.Jobs[0].Image)
	}
}

func TestParseDefaultStages(t *testing.T) {
	data := []byte(`
job1:
  image: alpine
  script: [echo hi]
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Jobs[0].Stage != "test" {
		t.Errorf("Stage = %q, want test", p.Jobs[0].Stage)
	}
	if len(p.Stages) != 5 {
		t.Errorf("default Stages = %v, want 5 default stages", p.Stages)
	}
}

func TestParseServicesTopLevelAndPerJob(t *testing.T) {
	data := []byte(`
services:
  - postgres:15

job1:
  image: alpine
  script: [echo hi]

job2:
  image: alpine
  script: [echo hi]
  services:
    - name: redis:7
      alias: cache
      variables:
        REDIS_PASSWORD: secret
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(p.Jobs))
	}

	job1 := p.Jobs[0]
	if len(job1.Services) != 1 || job1.Services[0].Image != "postgres:15" || job1.Services[0].Alias != "postgres" {
		t.Errorf("job1 (top-level default services) = %+v", job1.Services)
	}

	job2 := p.Jobs[1]
	if len(job2.Services) != 1 {
		t.Fatalf("job2 services = %+v, want 1 (job-level overrides top-level, doesn't merge)", job2.Services)
	}
	svc := job2.Services[0]
	if svc.Image != "redis:7" || svc.Alias != "cache" || svc.Variables["REDIS_PASSWORD"] != "secret" {
		t.Errorf("job2 service = %+v", svc)
	}
}

// TestParsePagesJobNotDropped proves a job literally named "pages" — an
// entirely ordinary job name in real GitLab CI, not a config keyword —
// is no longer silently dropped. See COMPATIBILITY.md.
func TestParsePagesJobNotDropped(t *testing.T) {
	data := []byte(`
pages:
  image: alpine
  script:
    - echo "publishing pages"
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 1 || p.Jobs[0].Name != "pages" {
		t.Fatalf("Jobs = %+v, want a single job named %q", p.Jobs, "pages")
	}
}

// TestParseExtendsSingleLevelMerged proves extends: is no longer
// silently ignored: a job extending a hidden template now inherits that
// template's before_script/variables, and the merge is recorded as an
// Emulated finding rather than passing through invisibly.
func TestParseExtendsSingleLevelMerged(t *testing.T) {
	data := []byte(`
.base:
  variables:
    FROM_BASE: yes
  before_script:
    - echo setting up

job1:
  extends: .base
  image: alpine
  script:
    - echo hi
  variables:
    JOB_OWN: yes
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1 (hidden .base must stay excluded): %+v", len(p.Jobs), p.Jobs)
	}

	job := p.Jobs[0]
	if job.Variables["FROM_BASE"] != "yes" {
		t.Errorf("job.Variables missing FROM_BASE inherited via extends: %+v", job.Variables)
	}
	if job.Variables["JOB_OWN"] != "yes" {
		t.Errorf("job.Variables missing the job's own JOB_OWN: %+v", job.Variables)
	}

	wantSteps := []string{"echo setting up", "echo hi"}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("job.Steps = %+v, want commands %v (before_script inherited via extends first)", job.Steps, wantSteps)
	}
	for i, cmd := range wantSteps {
		if job.Steps[i].Command != cmd {
			t.Errorf("Steps[%d].Command = %q, want %q", i, job.Steps[i].Command, cmd)
		}
	}

	found := false
	for _, f := range p.Findings {
		if f.Job == "job1" && f.Feature == "extends:" && f.Level == pipeline.Emulated {
			found = true
		}
	}
	if !found {
		t.Errorf("Findings = %+v, want an Emulated extends: finding for job1", p.Findings)
	}
}

// TestParseExtendsDeepChainFlaggedUnsupported proves that a chain of
// extends: deeper than one level is not silently accepted as if fully
// resolved — it's flagged as Unsupported instead, since only the first
// level actually gets merged.
func TestParseExtendsDeepChainFlaggedUnsupported(t *testing.T) {
	data := []byte(`
.base:
  before_script:
    - echo from base

.mid:
  extends: .base
  variables:
    FROM_MID: yes

job1:
  extends: .mid
  image: alpine
  script:
    - echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	found := false
	for _, f := range p.Findings {
		if f.Job == "job1" && f.Feature == "extends:" && f.Level == pipeline.Unsupported {
			found = true
		}
	}
	if !found {
		t.Errorf("Findings = %+v, want an Unsupported extends: finding for job1 (chain deeper than one level)", p.Findings)
	}
}

// TestParseExtendsThroughAnchorMerge reproduces the real-world pattern
// found in fdroidclient's .gitlab-ci.yml (see COMPATIBILITY.md): a real
// job doesn't write extends: itself, but merges in a hidden template via
// a plain YAML anchor (<<: *template), and that template is the one that
// uses extends:. Since the anchor merge is resolved natively by the YAML
// decoder before PolyCI's own code runs, the real job's raw fields end up
// containing that inherited extends: too — so single-level resolution is
// enough to correctly reach the base template's before_script here.
func TestParseExtendsThroughAnchorMerge(t *testing.T) {
	data := []byte(`
.base:
  before_script:
    - echo setting up

.test-template: &test-template
  extends: .base
  stage: test

job1:
  <<: *test-template
  image: alpine
  script:
    - echo hi
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1: %+v", len(p.Jobs), p.Jobs)
	}

	job := p.Jobs[0]
	if job.Stage != "test" {
		t.Errorf("job.Stage = %q, want %q (inherited from .test-template via the YAML anchor merge)", job.Stage, "test")
	}
	wantSteps := []string{"echo setting up", "echo hi"}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("job.Steps = %+v, want commands %v (before_script inherited from .base through .test-template)", job.Steps, wantSteps)
	}
	for i, cmd := range wantSteps {
		if job.Steps[i].Command != cmd {
			t.Errorf("Steps[%d].Command = %q, want %q", i, job.Steps[i].Command, cmd)
		}
	}
}
