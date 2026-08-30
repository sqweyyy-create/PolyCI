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

// Docker executes pipelines against a Docker Engine.
type Docker struct {
	cli *dockerclient.Client
	log Logger
}

// New connects to the local Docker Engine and returns a Docker executor. If
// DOCKER_HOST is unset, it resolves the host from the Docker CLI's active
// context (as `docker` itself does), so engines like Colima or Rancher
// Desktop that aren't the "default" context are found without the caller
// having to export DOCKER_HOST by hand.
func New(log Logger) (*Docker, error) {
	opts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		if host, err := activeContextDockerHost(); err == nil && host != "" {
			opts = append(opts, dockerclient.WithHost(host))
		}
	}
	cli, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("connect to Docker: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("Docker does not appear to be running: %w", err)
	}
	return &Docker{cli: cli, log: log}, nil
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
		if stepErr != nil {
			d.log.JobDone(job.Name, stepErr)
			return stepErr
		}
		if exitCode != 0 {
			err := fmt.Errorf("step %q exited with code %d", step.Name, exitCode)
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
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", step.Command},
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
