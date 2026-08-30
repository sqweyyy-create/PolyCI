package gitlab

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

func TestParseMissingStage(t *testing.T) {
	data := []byte(`
stages: [build]
job1:
  stage: deploy
  image: alpine
  script: [echo hi]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for undeclared stage, got nil")
	}
}

func TestParseMissingImage(t *testing.T) {
	data := []byte(`
job1:
  script: [echo hi]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for missing image, got nil")
	}
}

func TestParseMissingScript(t *testing.T) {
	data := []byte(`
job1:
  image: alpine
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for missing script, got nil")
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
