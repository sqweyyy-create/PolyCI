// Package pipeline defines the provider-agnostic pipeline model that every
// CI provider's parser converts into, so the Docker execution engine and the
// debugger are written once and reused across providers.
package pipeline

// Pipeline is a full CI run: an ordered list of stages (informational —
// see Job.Stage) and the jobs that belong to them.
type Pipeline struct {
	Stages []string
	Jobs   []Job
}

// Job is a single unit of work: run in one container, made of ordered steps.
// Stage is an informational label (shown in logs); actual run order and
// concurrency are driven entirely by DependsOn.
type Job struct {
	Name      string
	Stage     string
	Image     string
	Variables map[string]string
	Steps     []Step
	// DependsOn lists the names of other jobs in the same Pipeline that
	// must reach a terminal state (success, failure, or skip) before this
	// job may start. A job runs only if every dependency succeeded; if any
	// failed or was itself skipped, this job is skipped too. Jobs with no
	// common dependency relationship may run concurrently. For GitLab,
	// this is every job in the nearest non-empty preceding stage
	// (reproducing GitLab's stage-barrier semantics); for CircleCI and
	// GitHub Actions, it's the resolved requires:/needs: list.
	DependsOn []string
}

// Phase marks which part of a job a step belongs to, so the executor knows
// whether a failure there should stop the job or not.
type Phase int

const (
	// PhaseMain is a job's ordinary steps (GitLab's before_script and
	// script, or any other provider's steps — none of them distinguish
	// further phases today). The first failure among PhaseMain steps
	// stops the rest of PhaseMain.
	PhaseMain Phase = iota
	// PhaseAfter is GitLab's after_script: it always runs after PhaseMain
	// finishes, regardless of whether PhaseMain failed, since it exists
	// for cleanup/reporting that should happen either way.
	PhaseAfter
)

// Step is a single command executed inside the job's container. Env
// overrides/extends the job's own Variables for this step only (used by
// providers like CircleCI where `run` steps can set their own env). Phase
// defaults to PhaseMain, which is correct for every provider except
// GitLab's after_script.
type Step struct {
	Name    string
	Command string
	Env     map[string]string
	Phase   Phase
}
