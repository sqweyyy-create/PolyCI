// Package executor runs a provider-agnostic pipeline.Pipeline by creating a
// Docker container per job and executing its steps inside it via the Docker
// Engine API (never by shelling out to the docker CLI).
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/polyci/polyci/internal/pipeline"
)

const workDir = "/workspace"

// Logger receives streamed pipeline output. Both methods are called
// synchronously from the goroutine running the pipeline.
type Logger interface {
	// JobStart is called before a job's first step runs.
	JobStart(jobName, stage, image string)
	// StepStart is called before a step runs.
	StepStart(jobName, stepName, command string)
	// StepOutput is called with a chunk of a step's combined stdout/stderr.
	StepOutput(jobName, stepName string, chunk []byte)
	// StepDone is called after a step finishes.
	StepDone(jobName, stepName string, exitCode int64, err error)
	// JobDone is called after a job's steps finish (or one failed).
	JobDone(jobName string, err error)
}

// Decision is the debugger's answer to whether the pipeline should keep
// going after a step, returned from StepController.AfterStep.
type Decision int

const (
	// Continue moves on to the pipeline's next step, even one that
	// followed a failed step — a debugger may let the user override a
	// failure and keep going to investigate further.
	Continue Decision = iota
	// Abort stops the whole pipeline immediately.
	Abort
)

// ErrAborted is returned by Run/runJob when a StepController returns Abort
// for a step that had itself succeeded (an unprompted failure instead
// surfaces its own, more specific error).
var ErrAborted = fmt.Errorf("pipeline aborted by user")

// ShellFunc drops the caller into an interactive shell inside the
// container a step just ran in, wiring the real terminal's stdin/stdout to
// it. It blocks until the user exits the shell.
type ShellFunc func(ctx context.Context) error

// StepController is consulted after every step finishes, letting a
// debugger layer pause the pipeline, offer an interactive shell in the
// step's own container, and decide whether to continue or abort. When
// nil, the executor runs straight through and stops automatically at the
// first failing step (Phase 1 behavior).
type StepController interface {
	AfterStep(ctx context.Context, jobName string, step pipeline.Step, exitCode int64, stepErr error, shell ShellFunc) Decision
}

// Docker executes pipelines against a Docker Engine.
type Docker struct {
	cli  *dockerclient.Client
	log  Logger
	ctrl StepController
}

// Option configures optional behavior on a Docker executor.
type Option func(*Docker)

// WithController attaches a debugger layer that is asked, after every
// step, whether the pipeline should continue or abort.
func WithController(c StepController) Option {
	return func(d *Docker) { d.ctrl = c }
}

// New connects to the local Docker Engine and returns a Docker executor. If
// DOCKER_HOST is unset, it resolves the host from the Docker CLI's active
// context (as `docker` itself does), so engines like Colima or Rancher
// Desktop that aren't the "default" context are found without the caller
// having to export DOCKER_HOST by hand.
func New(log Logger, opts ...Option) (*Docker, error) {
	clientOpts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		if host, err := activeContextDockerHost(); err == nil && host != "" {
			clientOpts = append(clientOpts, dockerclient.WithHost(host))
		}
	}
	cli, err := dockerclient.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("connect to Docker: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("Docker does not appear to be running: %w", err)
	}
	d := &Docker{cli: cli, log: log}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// Close releases the underlying Docker client connection.
func (d *Docker) Close() error {
	return d.cli.Close()
}

// activeContextDockerHost mirrors how the Docker CLI resolves the daemon
// socket for the current context: read ~/.docker/config.json for the
// current context name, then look up that context's endpoint under
// ~/.docker/contexts/meta/<sha256(name)>/meta.json. Returns "" (with no
// error) for the built-in "default"/"" context, since that's what
// dockerclient.FromEnv already resolves on its own.
func activeContextDockerHost() (string, error) {
	dockerConfigDir := os.Getenv("DOCKER_CONFIG")
	if dockerConfigDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dockerConfigDir = filepath.Join(home, ".docker")
	}

	configData, err := os.ReadFile(filepath.Join(dockerConfigDir, "config.json"))
	if err != nil {
		return "", err
	}
	var config struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(configData, &config); err != nil {
		return "", err
	}
	if config.CurrentContext == "" || config.CurrentContext == "default" {
		return "", nil
	}

	hash := sha256.Sum256([]byte(config.CurrentContext))
	metaPath := filepath.Join(dockerConfigDir, "contexts", "meta", hex.EncodeToString(hash[:]), "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return "", err
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return "", err
	}
	return meta.Endpoints.Docker.Host, nil
}

// Run executes every stage of the pipeline in order; within a stage, jobs
// run sequentially in the order they were defined. Execution stops at the
// first failing job.
func (d *Docker) Run(ctx context.Context, p *pipeline.Pipeline) error {
	for _, stage := range p.Stages {
		for _, job := range p.JobsInStage(stage) {
			if err := d.runJob(ctx, job); err != nil {
				return fmt.Errorf("job %q failed: %w", job.Name, err)
			}
		}
	}
	return nil
}

func (d *Docker) runJob(ctx context.Context, job pipeline.Job) error {
	d.log.JobStart(job.Name, job.Stage, job.Image)

	if err := d.ensureImage(ctx, job.Image); err != nil {
		err = fmt.Errorf("pull image %q: %w", job.Image, err)
		d.log.JobDone(job.Name, err)
		return err
	}

	env := make([]string, 0, len(job.Variables))
	for k, v := range job.Variables {
		env = append(env, k+"="+v)
	}

	containerID, err := d.createContainer(ctx, job.Image, env)
	if err != nil {
		err = fmt.Errorf("create container: %w", err)
		d.log.JobDone(job.Name, err)
		return err
	}
	defer d.removeContainer(context.Background(), containerID)

	if err := d.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		err = fmt.Errorf("start container: %w", err)
		d.log.JobDone(job.Name, err)
		return err
	}

	for _, step := range job.Steps {
		d.log.StepStart(job.Name, step.Name, step.Command)
		exitCode, stepErr := d.execStep(ctx, containerID, job.Name, step)
		d.log.StepDone(job.Name, step.Name, exitCode, stepErr)

		failed := stepErr != nil || exitCode != 0
		failure := func() error {
			if stepErr != nil {
				return stepErr
			}
			return fmt.Errorf("step %q exited with code %d", step.Name, exitCode)
		}

		if d.ctrl != nil {
			shellFn := func(ctx context.Context) error { return d.Shell(ctx, containerID) }
			if d.ctrl.AfterStep(ctx, job.Name, step, exitCode, stepErr, shellFn) == Abort {
				err := ErrAborted
				if failed {
					err = failure()
				}
				d.log.JobDone(job.Name, err)
				return err
			}
			if failed {
				// The debugger let the user override the failure; move on.
				continue
			}
		} else if failed {
			err := failure()
			d.log.JobDone(job.Name, err)
			return err
		}
	}

	d.log.JobDone(job.Name, nil)
	return nil
}

func (d *Docker) ensureImage(ctx context.Context, ref string) error {
	_, err := d.cli.ImageInspect(ctx, ref)
	if err == nil {
		return nil
	}
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

func (d *Docker) createContainer(ctx context.Context, img string, env []string) (string, error) {
	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:      img,
		Env:        env,
		WorkingDir: workDir,
		Cmd:        []string{"sh", "-c", "mkdir -p " + workDir + " && tail -f /dev/null"},
		Tty:        false,
	}, nil, nil, nil, "")
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (d *Docker) removeContainer(ctx context.Context, id string) {
	_ = d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// execStep runs a single step's command inside the job's already-running
// container and streams its combined output through the Logger.
func (d *Docker) execStep(ctx context.Context, containerID, jobName string, step pipeline.Step) (int64, error) {
	env := make([]string, 0, len(step.Env))
	for k, v := range step.Env {
		env = append(env, k+"="+v)
	}

	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", step.Command},
		Env:          env,
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, fmt.Errorf("exec create: %w", err)
	}

	attach, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	stdoutW := &lineWriter{write: func(chunk []byte) { d.log.StepOutput(jobName, step.Name, chunk) }}
	if _, err := stdcopy.StdCopy(stdoutW, stdoutW, attach.Reader); err != nil && err != io.EOF {
		return -1, fmt.Errorf("read exec output: %w", err)
	}

	inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, fmt.Errorf("exec inspect: %w", err)
	}
	return int64(inspect.ExitCode), nil
}

// Shell drops the caller into an interactive shell inside the given
// container, wiring os.Stdin/os.Stdout to it over a Docker exec TTY. It
// prefers bash if the image has it, falling back to sh, and blocks until
// the shell exits.
func (d *Docker) Shell(ctx context.Context, containerID string) error {
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"},
		WorkingDir:   workDir,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	attach, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	restore := makeRawAndResize(ctx, d.cli, execResp.ID)
	defer restore()

	copyDone := make(chan struct{})
	go func() {
		io.Copy(attach.Conn, os.Stdin)
		// Local stdin closed (or hit EOF): tell the container's exec so a
		// shell reading non-interactively can exit instead of hanging.
		attach.CloseWrite()
		close(copyDone)
	}()
	io.Copy(os.Stdout, attach.Reader)

	select {
	case <-copyDone:
	case <-time.After(200 * time.Millisecond):
		// Stdin is likely still blocked on a read with no more input
		// coming; don't hang the pipeline waiting for it.
	}

	inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("shell exited with code %d", inspect.ExitCode)
	}
	return nil
}

// lineWriter forwards each Write call's bytes to write, unmodified — it
// exists only to satisfy io.Writer for stdcopy.StdCopy.
type lineWriter struct {
	write func([]byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.write(p)
	}
	return len(p), nil
}

var _ io.Writer = (*lineWriter)(nil)
