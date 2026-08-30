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

// Step is a single command executed inside the job's container. Env
// overrides/extends the job's own Variables for this step only (used by
// providers like CircleCI where `run` steps can set their own env).
type Step struct {
	Name    string
	Command string
	Env     map[string]string
}
