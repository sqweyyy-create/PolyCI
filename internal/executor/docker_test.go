package executor

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/polyci/polyci/internal/pipeline"
)

// recordingLogger captures step output for assertions instead of printing
// to a terminal.
type recordingLogger struct {
	mu     sync.Mutex
	output strings.Builder
	failed []string
}

func (l *recordingLogger) JobStart(jobName, stage, image string) {}
func (l *recordingLogger) StepStart(jobName, stepName, command string) {}

func (l *recordingLogger) StepOutput(jobName, stepName string, chunk []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.output.Write(chunk)
}

func (l *recordingLogger) StepDone(jobName, stepName string, exitCode int64, err error) {
	if err != nil || exitCode != 0 {
		l.mu.Lock()
		l.failed = append(l.failed, jobName+"/"+stepName)
		l.mu.Unlock()
	}
}

func (l *recordingLogger) JobDone(jobName string, err error) {}

func newExecutorOrSkip(t *testing.T, log Logger) *Docker {
	t.Helper()
	d, err := New(log)
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	return d
}

func TestRunSimplePipeline(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Stages: []string{"build", "test"},
		Jobs: []pipeline.Job{
			{
				Name:  "build-job",
				Stage: "build",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "echo from-build"},
				},
			},
			{
				Name:      "test-job",
				Stage:     "test",
				Image:     "alpine:3.19",
				Variables: map[string]string{"GREETING": "hi-from-var"},
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "echo $GREETING"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(log.failed) != 0 {
		t.Fatalf("unexpected failed steps: %v", log.failed)
	}
	out := log.output.String()
	if !strings.Contains(out, "from-build") {
		t.Errorf("output missing build step output: %q", out)
	}
	if !strings.Contains(out, "hi-from-var") {
		t.Errorf("output missing env var expansion: %q", out)
	}
}

func TestRunStopsOnFailure(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Stages: []string{"build", "test"},
		Jobs: []pipeline.Job{
			{
				Name:  "failing-job",
				Stage: "build",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "exit 1"},
				},
			},
			{
				Name:  "never-runs",
				Stage: "test",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "echo should-not-appear"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err == nil {
		t.Fatal("expected error from failing job, got nil")
	}
	if strings.Contains(log.output.String(), "should-not-appear") {
		t.Errorf("pipeline did not stop after failure: %q", log.output.String())
	}
}

// scriptedController returns a fixed sequence of decisions, one per
// AfterStep call, so tests can simulate a user answering prompts.
type scriptedController struct {
	decisions []Decision
	calls     []string
}

func (c *scriptedController) AfterStep(jobName string, step pipeline.Step, exitCode int64, stepErr error) Decision {
	c.calls = append(c.calls, jobName+"/"+step.Name)
	d := c.decisions[0]
	c.decisions = c.decisions[1:]
	return d
}

func TestControllerAbortStopsPipeline(t *testing.T) {
	log := &recordingLogger{}
	ctrl := &scriptedController{decisions: []Decision{Continue, Abort}}
	d, err := New(log, WithController(ctrl))
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	defer d.Close()

	p := &pipeline.Pipeline{
		Stages: []string{"build"},
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Stage: "build",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "echo first"},
					{Name: "script[1]", Command: "echo second"},
					{Name: "script[2]", Command: "echo third"},
				},
			},
		},
	}

	err = d.Run(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("Run() = %v, want an aborted error", err)
	}
	wantCalls := []string{"job1/script[0]", "job1/script[1]"}
	if strings.Join(ctrl.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("controller calls = %v, want %v (third step must not run)", ctrl.calls, wantCalls)
	}
	if strings.Contains(log.output.String(), "third") {
		t.Errorf("step after abort ran: %q", log.output.String())
	}
}

func TestControllerContinuePastFailure(t *testing.T) {
	log := &recordingLogger{}
	ctrl := &scriptedController{decisions: []Decision{Continue, Continue}}
	d, err := New(log, WithController(ctrl))
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	defer d.Close()

	p := &pipeline.Pipeline{
		Stages: []string{"build"},
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Stage: "build",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "script[0]", Command: "exit 7"},
					{Name: "script[1]", Command: "echo recovered"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v, want nil (user chose to continue past the failure)", err)
	}
	if !strings.Contains(log.output.String(), "recovered") {
		t.Errorf("step after continued-past failure did not run: %q", log.output.String())
	}
}
