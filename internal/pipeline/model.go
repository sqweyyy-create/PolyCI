// Package pipeline defines the provider-agnostic pipeline model that every
// CI provider's parser converts into, so the Docker execution engine and the
// debugger are written once and reused across providers.
package pipeline

// Pipeline is a full CI run: an ordered list of stages and the jobs that
// belong to them.
type Pipeline struct {
	Stages []string
	Jobs   []Job
}

// JobsInStage returns the jobs belonging to the given stage, in the order
// they were defined in the source config.
func (p *Pipeline) JobsInStage(stage string) []Job {
	var jobs []Job
	for _, j := range p.Jobs {
		if j.Stage == stage {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

// Job is a single unit of work: run in one container, made of ordered steps.
type Job struct {
	Name      string
	Stage     string
	Image     string
	Variables map[string]string
	Steps     []Step
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
