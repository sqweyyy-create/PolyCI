// Package circleci parses CircleCI's config.yml (2.1) into the same
// provider-agnostic pipeline model the GitLab parser produces, so it runs
// on the existing Docker executor and debugger unchanged.
package circleci

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/sqweyyy-create/PolyCI/internal/dag"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// noOpStepTypes are builtin CircleCI steps we don't implement (no local
// repo checkout, workspace/cache/artifact persistence, etc.). Rather than
// erroring on every real-world config that uses them, we turn them into a
// visible no-op step so the log is honest about what didn't happen.
// checkout is Emulated (the workspace mount is a real substitute for it);
// everything else is Unsupported (a pure no-op with no real substitute).
var noOpStepTypes = map[string]pipeline.FindingLevel{
	"checkout":             pipeline.Emulated,
	"setup_remote_docker":  pipeline.Unsupported,
	"persist_to_workspace": pipeline.Unsupported,
	"attach_workspace":     pipeline.Unsupported,
	"store_artifacts":      pipeline.Unsupported,
	"store_test_results":   pipeline.Unsupported,
	"save_cache":           pipeline.Unsupported,
	"restore_cache":        pipeline.Unsupported,
	"add_ssh_keys":         pipeline.Unsupported,
	"deploy":               pipeline.Unsupported,
}

// jobDef is a job as declared under the top-level `jobs:` key, before it's
// been placed into a workflow (which is what decides its stage).
type jobDef struct {
	image    string
	env      map[string]string
	steps    []pipeline.Step
	services []pipeline.Service
	findings []pipeline.Finding
}

// workflowJobRef is one entry in a workflow's `jobs:` list: which job
// definition to run, and which other jobs in the same workflow it requires.
type workflowJobRef struct {
	name     string
	requires []string
}

// Parse converts raw CircleCI config.yml bytes into a provider-agnostic
// pipeline.Pipeline. Only the docker executor is supported; workflow job
// dependencies (`requires:`) are resolved into dependency levels, which
// become the pipeline's stages so independent jobs at the same level run
// before anything that depends on them.
func Parse(data []byte) (*pipeline.Pipeline, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty config")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top level of config must be a mapping")
	}

	var jobsNode, workflowsNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "jobs":
			jobsNode = root.Content[i+1]
		case "workflows":
			workflowsNode = root.Content[i+1]
		}
	}
	if jobsNode == nil {
		return nil, fmt.Errorf("no top-level 'jobs' section found")
	}

	jobDefs, jobSkipReasons, err := parseJobs(jobsNode)
	if err != nil {
		return nil, err
	}

	workflowOrder, workflowJobs, err := parseWorkflows(workflowsNode, jobDefs, jobSkipReasons)
	if err != nil {
		return nil, err
	}

	p := &pipeline.Pipeline{}
	skippedSeen := map[string]bool{}
	for _, wfName := range workflowOrder {
		refs := workflowJobs[wfName]

		// A workflow job reference with no matching top-level jobs:
		// definition — either because that job failed to parse (a
		// job-level problem, e.g. a non-docker executor) or because it's
		// not defined under jobs: at all (most commonly: an orb-provided
		// job, which this parser doesn't expand) — is skipped rather than
		// failing the whole file. dag.FilterSkipped then cascades that
		// skip to any other job in the same workflow that requires: it,
		// since a job that depends on a job PolyCI can't run can't run
		// either; every job unrelated to it still runs normally.
		nodes := make([]dag.Node, len(refs))
		initialSkips := map[string]string{}
		for i, r := range refs {
			nodes[i] = dag.Node{Name: r.name, Depends: r.requires}
			if _, ok := jobDefs[r.name]; ok {
				continue
			}
			if reason, known := jobSkipReasons[r.name]; known {
				initialSkips[r.name] = reason
			} else {
				initialSkips[r.name] = "not defined under jobs: (orb-provided jobs and top-level commands: aren't supported)"
			}
		}
		keptNodes, allSkipReasons := dag.FilterSkipped(nodes, initialSkips)

		levels, order, err := dag.Levels(keptNodes)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", wfName, err)
		}

		maxLevel := 0
		for _, lvl := range levels {
			if lvl > maxLevel {
				maxLevel = lvl
			}
		}
		for lvl := 0; lvl <= maxLevel; lvl++ {
			p.Stages = append(p.Stages, fmt.Sprintf("%s/level-%d", wfName, lvl))
		}

		requiresByName := make(map[string][]string, len(refs))
		for _, r := range refs {
			requiresByName[r.name] = r.requires
		}

		for _, jobName := range order {
			def, ok := jobDefs[jobName]
			if !ok {
				return nil, fmt.Errorf("internal error: workflow %q's kept job %q has no definition", wfName, jobName)
			}
			p.Jobs = append(p.Jobs, pipeline.Job{
				Name:      jobName,
				Stage:     fmt.Sprintf("%s/level-%d", wfName, levels[jobName]),
				Image:     def.image,
				Variables: def.env,
				Steps:     def.steps,
				DependsOn: requiresByName[jobName],
				Services:  def.services,
			})
			p.Findings = append(p.Findings, def.findings...)
		}

		for _, r := range refs {
			if reason, skipped := allSkipReasons[r.name]; skipped && !skippedSeen[r.name] {
				skippedSeen[r.name] = true
				p.SkippedJobs = append(p.SkippedJobs, pipeline.SkippedJob{Name: r.name, Reason: reason})
			}
		}
	}

	if len(p.Jobs) == 0 && len(p.SkippedJobs) == 0 {
		return nil, fmt.Errorf("no runnable jobs found in config")
	}
	return p, nil
}

// parseWorkflows returns workflow names in declaration order and each
// one's job references. When there's no `workflows:` section at all, it
// falls back to CircleCI's legacy default: run the single job named
// "build" if one exists — including if "build" exists under jobs: but
// failed to parse, in which case the caller's usual per-workflow skip
// handling reports it rather than this function failing outright.
func parseWorkflows(workflowsNode *yaml.Node, jobDefs map[string]jobDef, jobSkipReasons map[string]string) ([]string, map[string][]workflowJobRef, error) {
	if workflowsNode == nil {
		_, defined := jobDefs["build"]
		_, skipped := jobSkipReasons["build"]
		if !defined && !skipped {
			return nil, nil, fmt.Errorf("no workflows section and no 'build' job found")
		}
		return []string{"default"}, map[string][]workflowJobRef{
			"default": {{name: "build"}},
		}, nil
	}

	if workflowsNode.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("workflows: must be a mapping")
	}

	var order []string
	jobs := map[string][]workflowJobRef{}
	for i := 0; i+1 < len(workflowsNode.Content); i += 2 {
		wfName := workflowsNode.Content[i].Value
		if wfName == "version" {
			continue // legacy `workflows: {version: 2}` marker, not a workflow
		}
		wfVal := workflowsNode.Content[i+1]
		if wfVal.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("workflow %q must be a mapping", wfName)
		}

		var jobsListNode *yaml.Node
		for j := 0; j+1 < len(wfVal.Content); j += 2 {
			if wfVal.Content[j].Value == "jobs" {
				jobsListNode = wfVal.Content[j+1]
			}
		}
		if jobsListNode == nil {
			return nil, nil, fmt.Errorf("workflow %q has no jobs", wfName)
		}

		refs, err := parseWorkflowJobs(jobsListNode)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow %q: %w", wfName, err)
		}

		order = append(order, wfName)
		jobs[wfName] = refs
	}

	return order, jobs, nil
}

func parseWorkflowJobs(node *yaml.Node) ([]workflowJobRef, error) {
	var items []interface{}
	if err := node.Decode(&items); err != nil {
		return nil, fmt.Errorf("jobs: must be a list: %w", err)
	}

	var refs []workflowJobRef
	for i, item := range items {
		switch v := item.(type) {
		case string:
			refs = append(refs, workflowJobRef{name: v})
		case map[string]interface{}:
			if len(v) != 1 {
				return nil, fmt.Errorf("jobs[%d]: expected a single job name key", i)
			}
			for name, params := range v {
				ref := workflowJobRef{name: name}
				if pm, ok := params.(map[string]interface{}); ok {
					if reqRaw, ok := pm["requires"]; ok {
						reqList, ok := reqRaw.([]interface{})
						if !ok {
							return nil, fmt.Errorf("jobs[%d]: requires must be a list", i)
						}
						for _, r := range reqList {
							if s, ok := r.(string); ok {
								ref.requires = append(ref.requires, s)
							}
						}
					}
				}
				refs = append(refs, ref)
			}
		default:
			return nil, fmt.Errorf("jobs[%d]: unsupported format", i)
		}
	}
	return refs, nil
}

// dependencyLevels assigns each job the length of its longest dependency
// chain (0 for a job with no requires), so jobs at the same level can run
// one after another while still guaranteeing every dependency's level is
// lower than its dependents'. It also returns a flat execution order:
// jobs grouped by ascending level, stable within a level by declaration
// order.
func dependencyLevels(refs []workflowJobRef) (map[string]int, []string, error) {
	nodes := make([]dag.Node, len(refs))
	for i, r := range refs {
		nodes[i] = dag.Node{Name: r.name, Depends: r.requires}
	}
	return dag.Levels(nodes)
}

// parseJobs parses every entry under the top-level jobs: mapping. A job
// that fails to parse (e.g. a non-docker executor) doesn't fail the whole
// config — its reason is recorded in the returned skip map instead, and
// the caller (Parse, via each workflow that references it) turns that into
// a SkippedJob rather than an error, so every other job still runs.
func parseJobs(node *yaml.Node) (defs map[string]jobDef, skipReasons map[string]string, err error) {
	if node.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("jobs: must be a mapping")
	}
	defs = map[string]jobDef{}
	skipReasons = map[string]string{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		def, err := parseJob(name, node.Content[i+1])
		if err != nil {
			skipReasons[name] = err.Error()
			continue
		}
		defs[name] = def
	}
	return defs, skipReasons, nil
}

func parseJob(jobName string, node *yaml.Node) (jobDef, error) {
	raw := map[string]interface{}{}
	if err := node.Decode(&raw); err != nil {
		return jobDef{}, err
	}

	image, services, err := jobImageAndServices(raw)
	if err != nil {
		return jobDef{}, err
	}

	stepsRaw, ok := raw["steps"]
	if !ok {
		return jobDef{}, fmt.Errorf("no steps defined")
	}
	stepsList, ok := stepsRaw.([]interface{})
	if !ok {
		return jobDef{}, fmt.Errorf("steps must be a list")
	}

	var steps []pipeline.Step
	var findings []pipeline.Finding
	for idx, item := range stepsList {
		step, finding, err := parseStep(jobName, idx, item)
		if err != nil {
			return jobDef{}, err
		}
		steps = append(steps, step)
		if finding != nil {
			findings = append(findings, *finding)
		}
	}
	if len(steps) == 0 {
		return jobDef{}, fmt.Errorf("steps must not be empty")
	}

	return jobDef{image: image, env: toStringMap(raw["environment"]), steps: steps, services: services, findings: findings}, nil
}

// jobImageAndServices reads a job's docker: list: the first entry is the
// job's own image; any entries after it are secondary containers started
// alongside it as services, reachable by their name: (or, if unset,
// pipeline.DefaultServiceAlias(image)).
func jobImageAndServices(raw map[string]interface{}) (string, []pipeline.Service, error) {
	dockerRaw, ok := raw["docker"]
	if !ok {
		return "", nil, fmt.Errorf("only the docker executor is supported (job has no docker: key)")
	}
	dockerList, ok := dockerRaw.([]interface{})
	if !ok || len(dockerList) == 0 {
		return "", nil, fmt.Errorf("docker: must be a non-empty list")
	}
	first, ok := dockerList[0].(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("docker: first entry must be a mapping with an image: key")
	}
	image, ok := first["image"].(string)
	if !ok || image == "" {
		return "", nil, fmt.Errorf("docker: first entry is missing image:")
	}

	var services []pipeline.Service
	for i, entry := range dockerList[1:] {
		m, ok := entry.(map[string]interface{})
		if !ok {
			return "", nil, fmt.Errorf("docker[%d]: must be a mapping", i+1)
		}
		svcImage, ok := m["image"].(string)
		if !ok || svcImage == "" {
			return "", nil, fmt.Errorf("docker[%d]: missing image:", i+1)
		}
		alias, _ := m["name"].(string)
		if alias == "" {
			alias = pipeline.DefaultServiceAlias(svcImage)
		}
		services = append(services, pipeline.Service{
			Image:     svcImage,
			Alias:     alias,
			Variables: toStringMap(m["environment"]),
		})
	}

	return image, services, nil
}

func parseStep(jobName string, idx int, item interface{}) (pipeline.Step, *pipeline.Finding, error) {
	switch v := item.(type) {
	case string:
		return noOpStep(jobName, idx, v)
	case map[string]interface{}:
		if len(v) != 1 {
			return pipeline.Step{}, nil, fmt.Errorf("step %d: expected a single step type key, got %d", idx, len(v))
		}
		for stepType, params := range v {
			if stepType == "run" {
				step, err := parseRunStep(idx, params)
				return step, nil, err
			}
			return noOpStep(jobName, idx, stepType)
		}
	}
	return pipeline.Step{}, nil, fmt.Errorf("step %d: unsupported step format", idx)
}

func parseRunStep(idx int, params interface{}) (pipeline.Step, error) {
	switch p := params.(type) {
	case string:
		return pipeline.Step{Name: fmt.Sprintf("run[%d]", idx), Command: p}, nil
	case map[string]interface{}:
		command, _ := p["command"].(string)
		if command == "" {
			return pipeline.Step{}, fmt.Errorf("step %d: run: missing command", idx)
		}
		name := fmt.Sprintf("run[%d]", idx)
		if n, ok := p["name"].(string); ok && n != "" {
			name = n
		}
		return pipeline.Step{Name: name, Command: command, Env: toStringMap(p["environment"])}, nil
	default:
		return pipeline.Step{}, fmt.Errorf("step %d: run: unsupported format", idx)
	}
}

// noOpStep turns an unsupported builtin step (checkout, save_cache, etc.)
// into a visible, harmless log line rather than erroring or silently
// dropping it — see the "Known limitations" note in CLAUDE.md — and
// records a Finding for `polyci check` to report.
func noOpStep(jobName string, idx int, stepType string) (pipeline.Step, *pipeline.Finding, error) {
	level, ok := noOpStepTypes[stepType]
	if !ok {
		return pipeline.Step{}, nil, fmt.Errorf("step %d: unsupported step type %q", idx, stepType)
	}
	reason := fmt.Sprintf("step '%s' is not supported yet, skipping", stepType)
	if stepType == "checkout" {
		reason = "checkout is a no-op: the repo is already mounted at /workspace"
	}
	stepName := fmt.Sprintf("%s[%d]", stepType, idx)
	step := pipeline.Step{
		Name:    stepName,
		Command: fmt.Sprintf("echo \"polyci: %s\"", reason),
	}
	finding := &pipeline.Finding{
		Job:     jobName,
		Step:    stepName,
		Feature: stepType,
		Level:   level,
		Detail:  reason,
	}
	return step, finding, nil
}

// toStringMap converts an already-decoded generic map[string]interface{}
// value into a map[string]string, stringifying scalar values.
func toStringMap(v interface{}) map[string]string {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = fmt.Sprintf("%v", val)
	}
	return out
}
