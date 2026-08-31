package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// recordingLogger captures step output for assertions instead of printing
// to a terminal. Safe for concurrent use, since independent jobs now run
// concurrently and may call it from multiple goroutines at once.
type recordingLogger struct {
	mu      sync.Mutex
	output  strings.Builder
	failed  []string
	started []string
	skipped []string
}

func (l *recordingLogger) JobStart(jobName, stage, image string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, jobName)
}
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

func (l *recordingLogger) JobSkipped(jobName string, reason error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.skipped = append(l.skipped, jobName)
}

func (l *recordingLogger) wasStarted(jobName string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range l.started {
		if n == jobName {
			return true
		}
	}
	return false
}

func (l *recordingLogger) wasSkipped(jobName string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range l.skipped {
		if n == jobName {
			return true
		}
	}
	return false
}

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
				DependsOn: []string{"build-job"},
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
				DependsOn: []string{"failing-job"},
			},
		},
	}

	if err := d.Run(context.Background(), p); err == nil {
		t.Fatal("expected error from failing job, got nil")
	}
	if strings.Contains(log.output.String(), "should-not-appear") {
		t.Errorf("pipeline did not stop after failure: %q", log.output.String())
	}
	if log.wasStarted("never-runs") {
		t.Error("never-runs should have been skipped, not started")
	}
	if !log.wasSkipped("never-runs") {
		t.Error("never-runs should have been reported as skipped")
	}
}

// scriptedController returns a fixed sequence of decisions, one per
// AfterStep call, so tests can simulate a user answering prompts.
type scriptedController struct {
	decisions []Decision
	calls     []string
}

func (c *scriptedController) AfterStep(ctx context.Context, jobName string, step pipeline.Step, exitCode int64, stepErr error, shell ShellFunc) Decision {
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

// TestControllerRetrySucceedsOnSecondAttempt proves Retry actually re-runs
// the same step in the same container — not just accepts a mocked result —
// and that a retry which succeeds lets the pipeline proceed normally to
// the next step. The step's command is stateful: it fails and leaves a
// marker file on its first invocation, then succeeds once that marker is
// already there, so a second real execution is required for it to pass.
func TestControllerRetrySucceedsOnSecondAttempt(t *testing.T) {
	hostDir := hostMountableTempDir(t)

	log := &recordingLogger{}
	ctrl := &scriptedController{decisions: []Decision{Retry, Continue, Continue}}
	d, err := New(log, WithController(ctrl), WithWorkspace(hostDir))
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "flaky", Command: "test -f retried && exit 0 || (touch retried && exit 1)"},
					{Name: "after", Command: "echo made it to the next step"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v, want nil (the retried step should have succeeded on its second attempt)", err)
	}

	wantCalls := []string{"job1/flaky", "job1/flaky", "job1/after"}
	if strings.Join(ctrl.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("controller calls = %v, want %v (flaky step invoked twice via retry, then the pipeline moves on)", ctrl.calls, wantCalls)
	}
	if !strings.Contains(log.output.String(), "made it to the next step") {
		t.Errorf("pipeline did not proceed to the step after the retried one: %q", log.output.String())
	}
}

// hostMountableTempDir returns a temp directory under $HOME rather than
// t.TempDir()'s OS temp dir. Docker engines that run in a VM sharing only
// $HOME with the host (Colima's default) can't bind-mount paths outside it
// — see the "Known limitations" note in CLAUDE.md.
func hostMountableTempDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".polyci-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestWorkspaceMountReadWrite verifies jobs run against real project files
// on the host (read) and that files a job writes are visible back on the
// host afterward (write) — the workspace bind mount added to fix the "jobs
// don't see your repo" limitation noted in CLAUDE.md.
func TestWorkspaceMountReadWrite(t *testing.T) {
	hostDir := hostMountableTempDir(t)
	if err := os.WriteFile(filepath.Join(hostDir, "greeting.txt"), []byte("hello from the host"), 0o644); err != nil {
		t.Fatal(err)
	}

	log := &recordingLogger{}
	d, err := New(log, WithWorkspace(hostDir))
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
					{Name: "read", Command: "cat greeting.txt"},
					{Name: "write", Command: "echo 'hello from the container' > from-container.txt"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(log.output.String(), "hello from the host") {
		t.Errorf("container did not see the host file's real content: %q", log.output.String())
	}

	written, err := os.ReadFile(filepath.Join(hostDir, "from-container.txt"))
	if err != nil {
		t.Fatalf("file written by the container is not visible on the host: %v", err)
	}
	if got := strings.TrimSpace(string(written)); got != "hello from the container" {
		t.Errorf("host-visible file content = %q, want %q", got, "hello from the container")
	}
}

// TestAfterScriptRunsOnFailure proves the fix for after_script: it must
// run even when a PhaseMain (script) step fails, and the job must still
// report that failure once after_script has had its chance to run.
func TestAfterScriptRunsOnFailure(t *testing.T) {
	hostDir := hostMountableTempDir(t)

	log := &recordingLogger{}
	d, err := New(log, WithWorkspace(hostDir))
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
					{Name: "script[0]", Command: "false", Phase: pipeline.PhaseMain},
					{Name: "after_script[0]", Command: "touch /workspace/after-ran", Phase: pipeline.PhaseAfter},
				},
			},
		},
	}

	err = d.Run(context.Background(), p)
	if err == nil {
		t.Fatal("Run() = nil, want an error: the script step failed and the job should still report it")
	}

	if _, statErr := os.Stat(filepath.Join(hostDir, "after-ran")); statErr != nil {
		t.Fatalf("after_script did not run despite the script failure (after-ran not found on host): %v", statErr)
	}
}

// timingLogger records each job's [start, end) wall-clock window, so a
// test can prove two jobs actually overlapped in time rather than merely
// being declared independent by the scheduler.
type timingLogger struct {
	mu    sync.Mutex
	spans map[string][2]time.Time
}

func newTimingLogger() *timingLogger {
	return &timingLogger{spans: map[string][2]time.Time{}}
}

func (l *timingLogger) JobStart(jobName, stage, image string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.spans[jobName]
	s[0] = time.Now()
	l.spans[jobName] = s
}
func (l *timingLogger) StepStart(jobName, stepName, command string)                  {}
func (l *timingLogger) StepOutput(jobName, stepName string, chunk []byte)            {}
func (l *timingLogger) StepDone(jobName, stepName string, exitCode int64, err error) {}

func (l *timingLogger) JobDone(jobName string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.spans[jobName]
	s[1] = time.Now()
	l.spans[jobName] = s
}
func (l *timingLogger) JobSkipped(jobName string, reason error) {}

// overlaps reports whether jobs a and b's [start, end) windows intersected
// — i.e. a started before b ended, and b started before a ended.
func (l *timingLogger) overlaps(a, b string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	sa, sb := l.spans[a], l.spans[b]
	return sa[0].Before(sb[1]) && sb[0].Before(sa[1])
}

// TestDiamondDependencyRunsIndependentJobsConcurrently builds a diamond
// DAG (A -> B, A -> C, B and C -> D) and proves B and C — which both
// depend only on A and have no relationship to each other — actually run
// concurrently, not just that the scheduler treats them as independent.
// Each sleeps for 2 seconds: if they ran one after another the whole
// pipeline would take roughly twice as long as if they overlapped, and
// their real wall-clock [start,end) windows wouldn't intersect.
func TestDiamondDependencyRunsIndependentJobsConcurrently(t *testing.T) {
	log := newTimingLogger()
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{Name: "A", Image: "alpine:3.19", Steps: []pipeline.Step{{Name: "s", Command: "echo A"}}},
			{Name: "B", Image: "alpine:3.19", DependsOn: []string{"A"}, Steps: []pipeline.Step{{Name: "s", Command: "sleep 2"}}},
			{Name: "C", Image: "alpine:3.19", DependsOn: []string{"A"}, Steps: []pipeline.Step{{Name: "s", Command: "sleep 2"}}},
			{Name: "D", Image: "alpine:3.19", DependsOn: []string{"B", "C"}, Steps: []pipeline.Step{{Name: "s", Command: "echo D"}}},
		},
	}

	start := time.Now()
	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	// Sequential execution would be roughly A + B(2s) + C(2s) + D, north
	// of 4s of sleeping alone before any Docker overhead. Concurrent
	// execution should be roughly A + max(B,C)(2s) + D. This ceiling sits
	// clearly below the sequential total and comfortably above the
	// concurrent expectation.
	if elapsed >= 4500*time.Millisecond {
		t.Errorf("pipeline took %v, too long for B and C (each sleeps 2s) to have run concurrently", elapsed)
	}

	if !log.overlaps("B", "C") {
		t.Errorf("B and C did not overlap in wall-clock time: B=%v C=%v", log.spans["B"], log.spans["C"])
	}
}

// TestDiamondDependencyFailurePropagation builds the same diamond DAG but
// with B failing, and proves: D (which depends on both B and C) is
// skipped without ever starting, while C — B's sibling, sharing only A as
// a dependency and otherwise unrelated to B — still runs to completion
// unaffected by B's failure.
func TestDiamondDependencyFailurePropagation(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{Name: "A", Image: "alpine:3.19", Steps: []pipeline.Step{{Name: "s", Command: "echo A"}}},
			{Name: "B", Image: "alpine:3.19", DependsOn: []string{"A"}, Steps: []pipeline.Step{{Name: "s", Command: "exit 1"}}},
			{Name: "C", Image: "alpine:3.19", DependsOn: []string{"A"}, Steps: []pipeline.Step{{Name: "s", Command: "echo C ran fine"}}},
			{Name: "D", Image: "alpine:3.19", DependsOn: []string{"B", "C"}, Steps: []pipeline.Step{{Name: "s", Command: "echo should-not-run"}}},
		},
	}

	err := d.Run(context.Background(), p)
	if err == nil {
		t.Fatal("Run() = nil, want an error (B failed)")
	}

	if !log.wasStarted("A") {
		t.Error("A should have started")
	}
	if !log.wasStarted("B") {
		t.Error("B should have started (and then failed)")
	}
	if !log.wasStarted("C") {
		t.Error("C (B's independent sibling) should have run despite B's failure")
	}
	if !strings.Contains(log.output.String(), "C ran fine") {
		t.Errorf("C's output is missing — sibling branch did not complete: %q", log.output.String())
	}
	if log.wasStarted("D") {
		t.Error("D should have been skipped, not started")
	}
	if !log.wasSkipped("D") {
		t.Error("D should have been reported as skipped")
	}
	if strings.Contains(log.output.String(), "should-not-run") {
		t.Errorf("D's step ran despite being skipped: %q", log.output.String())
	}
}

// TestServiceContainerReachableByAlias proves service containers are
// genuinely reachable from the job's container by their configured
// alias — not just that a container gets created. It uses a real
// postgres server as the service and has the job connect to it over a
// real TCP connection via psql, addressing it purely by the hostname
// "postgres" (the alias), which only resolves if the job's container was
// actually attached to a network shared with the service and Docker's
// embedded DNS is doing real alias resolution. The job's own image is
// also postgres, since that image conveniently bundles the psql client.
func TestServiceContainerReachableByAlias(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	const pgImage = "postgres:16-alpine"

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Image: pgImage,
				Services: []pipeline.Service{
					{
						Image: pgImage,
						Alias: "postgres",
						Variables: map[string]string{
							"POSTGRES_USER":     "testuser",
							"POSTGRES_PASSWORD": "testpass",
							"POSTGRES_DB":       "testdb",
						},
					},
				},
				Variables: map[string]string{"PGPASSWORD": "testpass"},
				Steps: []pipeline.Step{
					{Name: "connect", Command: `
for i in $(seq 1 30); do
  if psql -h postgres -U testuser -d testdb -c "SELECT 1" >/dev/null 2>&1; then
    echo CONNECTED_TO_POSTGRES_VIA_ALIAS
    exit 0
  fi
  sleep 1
done
echo TIMED_OUT_WAITING_FOR_POSTGRES
exit 1
`},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, log.output.String())
	}
	if !strings.Contains(log.output.String(), "CONNECTED_TO_POSTGRES_VIA_ALIAS") {
		t.Errorf("job did not connect to the postgres service via its alias: %q", log.output.String())
	}
}

// TestStepShellBashRunsUnderBash proves a step's Shell field actually
// selects the interpreter it runs under, not just gets recorded and
// ignored: bash array syntax is not valid in the "bash" image's other
// shell (sh is a POSIX-only symlink there), so this command only succeeds
// if it genuinely ran via bash -c rather than the previous hardcoded
// sh -c.
func TestStepShellBashRunsUnderBash(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Image: "bash:5.2",
				Steps: []pipeline.Step{
					{
						Name:    "bash-only",
						Command: `arr=(one two three); echo "BASH_ARRAY_ELEMENT=${arr[1]}"`,
						Shell:   "bash",
					},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, log.output.String())
	}
	if !strings.Contains(log.output.String(), "BASH_ARRAY_ELEMENT=two") {
		t.Errorf("step did not run under bash: %q", log.output.String())
	}
}

// TestStepShellUnsupportedFailsClearly proves requesting an unsupported
// shell (e.g. GitHub Actions' pwsh) is a clear, immediate failure rather
// than a silent fallback to sh that would run the command under the wrong
// interpreter without telling anyone.
func TestStepShellUnsupportedFailsClearly(t *testing.T) {
	log := &recordingLogger{}
	d := newExecutorOrSkip(t, log)
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "pwsh-step", Command: "Write-Host hi", Shell: "pwsh"},
				},
			},
		},
	}

	err := d.Run(context.Background(), p)
	if err == nil {
		t.Fatal("expected an error for an unsupported shell, got nil")
	}
	if !strings.Contains(err.Error(), "pwsh") {
		t.Errorf("error does not name the unsupported shell: %v", err)
	}
	if strings.Contains(log.output.String(), "hi") {
		t.Errorf("step must not have actually run under any shell: %q", log.output.String())
	}
}

// TestStepWorkingDirectoryUsedAsCwd proves a step's WorkingDirectory field
// actually changes the container's exec working directory, not just gets
// recorded and ignored: a relative path only resolves if the exec's cwd is
// genuinely the subdirectory, not the workspace root the executor
// previously always used.
func TestStepWorkingDirectoryUsedAsCwd(t *testing.T) {
	hostDir := hostMountableTempDir(t)
	subDir := filepath.Join(hostDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "marker.txt"), []byte("found in subdir"), 0o644); err != nil {
		t.Fatal(err)
	}

	log := &recordingLogger{}
	d, err := New(log, WithWorkspace(hostDir))
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	defer d.Close()

	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "job1",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					// A relative path only resolves if the cwd is really
					// subdir/, not /workspace.
					{Name: "read-relative", Command: "cat marker.txt", WorkingDirectory: "subdir"},
				},
			},
		},
	}

	if err := d.Run(context.Background(), p); err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, log.output.String())
	}
	if !strings.Contains(log.output.String(), "found in subdir") {
		t.Errorf("step did not run with WorkingDirectory as its cwd: %q", log.output.String())
	}
}
