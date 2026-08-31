// Package pipeline defines the provider-agnostic pipeline model that every
// CI provider's parser converts into, so the Docker execution engine and the
// debugger are written once and reused across providers.
package pipeline

import "strings"

// Pipeline is a full CI run: an ordered list of stages (informational —
// see Job.Stage) and the jobs that belong to them.
type Pipeline struct {
	Stages []string
	Jobs   []Job
	// Findings records config features the parser recognized as not
	// fully faithful to the real provider — for `polyci check` to report.
	// Populating this never changes what Run executes; it's purely
	// informational. A parser that finds nothing to flag leaves this nil.
	Findings []Finding
}

// FindingLevel classifies how faithfully PolyCI handles a recognized
// config feature.
type FindingLevel int

const (
	// Emulated means the feature was translated into something that
	// approximates real behavior rather than faithfully implementing it —
	// e.g. a no-op step standing in for a real checkout, since the
	// workspace mount already puts the repo's files in place.
	Emulated FindingLevel = iota
	// Unsupported means the feature was recognized but isn't implemented
	// at all; its presence may cause the job to behave differently than
	// it would on the real provider.
	Unsupported
)

// String returns a short label for the level, used in `polyci check`'s
// output.
func (l FindingLevel) String() string {
	switch l {
	case Emulated:
		return "Emulated"
	case Unsupported:
		return "Unsupported"
	default:
		return "Unknown"
	}
}

// Finding records one specific feature of the source config and how
// faithfully PolyCI handles it. Job and Step name the location it was
// found at; Step is empty for a job-level or pipeline-level finding (Job
// is then also empty).
type Finding struct {
	Job     string
	Step    string
	Feature string
	Level   FindingLevel
	Detail  string
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
	// Services are additional containers started alongside the job's own
	// container for its duration, reachable from it by their Alias (e.g.
	// GitLab's services:, or CircleCI's docker: entries after the first).
	Services []Service
}

// Service is a secondary container started alongside a job's own
// container, on a network shared with it, so the job can reach it by
// hostname (Alias) — e.g. a database the job's tests connect to.
type Service struct {
	Image     string
	Alias     string
	Variables map[string]string
}

// DefaultServiceAlias derives the hostname a service is reachable by when
// no explicit alias is configured, following the same convention GitLab
// and CircleCI both use: the image name without its registry path or
// tag/digest. "postgres:15" and "docker.io/library/postgres:15" both give
// "postgres".
func DefaultServiceAlias(image string) string {
	name := image
	if i := strings.LastIndex(name, "/"); i != -1 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, "@"); i != -1 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, ":"); i != -1 {
		name = name[:i]
	}
	return name
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
