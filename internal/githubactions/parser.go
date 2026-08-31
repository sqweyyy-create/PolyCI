// Package githubactions parses a GitHub Actions workflow file into the
// same provider-agnostic pipeline model the other parsers produce, so it
// runs on the existing Docker executor, debugger, and workspace mount
// unchanged.
//
// GitHub Actions' default runner model executes steps directly on a VM
// (`runs-on: ubuntu-latest`), not in a container — the opposite of GitLab
// CI and CircleCI, which are container-native. Since this whole tool is
// Docker-only by design, only jobs that opt in with an explicit
// `container:` are runnable; see the "Known limitations" note in
// CLAUDE.md.
package githubactions

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sqweyyy-create/PolyCI/internal/dag"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// noOpUsesPrefixes are `uses:` actions we don't execute (running arbitrary
// third-party actions is out of scope — that's `act`'s job). Rather than
// erroring on every real-world workflow that uses them, they become a
// visible no-op step so the log is honest about what didn't happen.
var noOpUsesPrefixes = []string{
	"actions/checkout",
}

// Parse converts raw GitHub Actions workflow YAML bytes into a
// provider-agnostic pipeline.Pipeline, resolving expressions and expanding
// matrix jobs against the git repository at the current working
// directory — the same directory that becomes /workspace when the pipeline
// runs, since the executor always bind-mounts the process's cwd there.
func Parse(data []byte) (*pipeline.Pipeline, error) {
	return parseInDir(data, ".")
}

// parseInDir is Parse's actual implementation, taking the directory
// github.sha/github.ref are read from explicitly — a separate entry point
// so tests can point it at a throwaway git repository without touching the
// process's real working directory.
//
// Job `needs:` dependencies are resolved into dependency levels, which
// become the pipeline's stages so independent jobs at the same level run
// before anything that depends on them. A job with a strategy.matrix
// expands into one job per combination before dependency levels are
// computed, and a job that `needs:` a matrix job depends on every one of
// its combinations.
func parseInDir(data []byte, dir string) (*pipeline.Pipeline, error) {
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

	var jobsNode *yaml.Node
	globalEnv := map[string]string{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "jobs":
			jobsNode = root.Content[i+1]
		case "env":
			globalEnv = toStringMap(decodeInterface(root.Content[i+1]))
		}
	}
	if jobsNode == nil {
		return nil, fmt.Errorf("no top-level 'jobs' section found")
	}
	if jobsNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("jobs: must be a mapping")
	}

	git := loadGitInfo(dir)

	// Each raw job in jobs: (a "base name") expands into one or more
	// jobDefs (more than one only if it has a strategy.matrix). expansions
	// records which final, unique job names a base name produced, so a
	// needs: reference to a base name can be resolved into the full list
	// of jobs it now actually depends on.
	defs := map[string]jobDef{}
	expansions := map[string][]string{}
	baseNeeds := map[string][]string{}
	var baseNames []string
	for i := 0; i+1 < len(jobsNode.Content); i += 2 {
		baseName := jobsNode.Content[i].Value
		jobDefs, needs, err := parseJob(baseName, jobsNode.Content[i+1], globalEnv, git)
		if err != nil {
			return nil, fmt.Errorf("job %q: %w", baseName, err)
		}
		names := make([]string, len(jobDefs))
		for j, d := range jobDefs {
			defs[d.name] = d
			names[j] = d.name
		}
		expansions[baseName] = names
		baseNeeds[baseName] = needs
		baseNames = append(baseNames, baseName)
	}

	var nodes []dag.Node
	needsByName := map[string][]string{}
	for _, baseName := range baseNames {
		for _, name := range expansions[baseName] {
			var resolved []string
			for _, needBase := range baseNeeds[baseName] {
				needNames, ok := expansions[needBase]
				if !ok {
					return nil, fmt.Errorf("job %q: needs: references unknown job %q", baseName, needBase)
				}
				resolved = append(resolved, needNames...)
			}
			needsByName[name] = resolved
			nodes = append(nodes, dag.Node{Name: name, Depends: resolved})
		}
	}

	levels, order, err := dag.Levels(nodes)
	if err != nil {
		return nil, err
	}

	p := &pipeline.Pipeline{}
	maxLevel := 0
	for _, lvl := range levels {
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}
	for lvl := 0; lvl <= maxLevel; lvl++ {
		p.Stages = append(p.Stages, fmt.Sprintf("level-%d", lvl))
	}

	for _, name := range order {
		def := defs[name]
		p.Jobs = append(p.Jobs, pipeline.Job{
			Name:      name,
			Stage:     fmt.Sprintf("level-%d", levels[name]),
			Image:     def.image,
			Variables: def.env,
			Steps:     def.steps,
			DependsOn: needsByName[name],
		})
		p.Findings = append(p.Findings, def.findings...)
	}

	if len(p.Jobs) == 0 {
		return nil, fmt.Errorf("no runnable jobs found in config")
	}
	return p, nil
}

// jobDef is one concrete, fully-resolved job — for a job with no
// strategy.matrix, parseJob produces exactly one; for a job with an N-way
// matrix, it produces N, one per combination, each with its own name.
type jobDef struct {
	name     string
	image    string
	env      map[string]string
	steps    []pipeline.Step
	findings []pipeline.Finding
}

// parseJob parses one raw job entry into one jobDef per matrix combination
// (or a single jobDef if it has no strategy.matrix), substituting
// matrix./env./github. expressions in its image, env values, and steps
// along the way. It returns the job's own (still base-name) needs: list —
// the caller resolves that against every other job's own expansion, since
// a matrix job's dependents must wait for all of its combinations.
func parseJob(jobName string, node *yaml.Node, globalEnv map[string]string, git gitInfo) ([]jobDef, []string, error) {
	raw, ok := decodeInterface(node).(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("must be a mapping")
	}

	rawImage, err := jobImage(raw)
	if err != nil {
		return nil, nil, err
	}

	baseEnv := map[string]string{}
	for k, v := range globalEnv {
		baseEnv[k] = v
	}
	for k, v := range toStringMap(raw["env"]) {
		baseEnv[k] = v
	}
	if c, ok := raw["container"].(map[string]interface{}); ok {
		for k, v := range toStringMap(c["env"]) {
			baseEnv[k] = v
		}
	}

	stepsRaw, ok := raw["steps"]
	if !ok {
		return nil, nil, fmt.Errorf("no steps defined")
	}
	stepsList, ok := stepsRaw.([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("steps must be a list")
	}

	combos, hasMatrix, matrixFindings := matrixCombinations(jobName, raw)

	var defs []jobDef
	for i, combo := range combos {
		name := jobName
		if hasMatrix && len(combo) > 0 {
			name = fmt.Sprintf("%s (%s)", jobName, comboLabel(combo))
		}

		var findings []pipeline.Finding
		if i == 0 {
			// Job-level findings (e.g. matrix.include:/exclude:) apply to
			// the job as a whole, not to any one combination — attach them
			// once rather than duplicating across every combination.
			findings = append(findings, matrixFindings...)
		}

		ctx := exprContext{matrix: combo, git: git}

		image, fs := substituteExpressions(rawImage, name, "", ctx)
		findings = append(findings, fs...)

		env := make(map[string]string, len(baseEnv))
		for k, v := range baseEnv {
			sv, fs := substituteExpressions(v, name, "", ctx)
			env[k] = sv
			findings = append(findings, fs...)
		}
		ctx.env = env

		var steps []pipeline.Step
		for idx, item := range stepsList {
			stepRaw, ok := item.(map[string]interface{})
			if !ok {
				return nil, nil, fmt.Errorf("step %d: must be a mapping", idx)
			}
			step, stepFindings, err := parseStep(name, idx, stepRaw, ctx)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, step)
			findings = append(findings, stepFindings...)
		}
		if len(steps) == 0 {
			return nil, nil, fmt.Errorf("steps must not be empty")
		}

		defs = append(defs, jobDef{name: name, image: image, env: env, steps: steps, findings: findings})
	}

	return defs, toStringSlice(raw["needs"]), nil
}

func jobImage(raw map[string]interface{}) (string, error) {
	c, ok := raw["container"]
	if !ok {
		return "", fmt.Errorf("no container: specified — running jobs on a bare runs-on: runner (without an explicit container image) is not supported")
	}
	switch v := c.(type) {
	case string:
		if v == "" {
			return "", fmt.Errorf("container: must not be empty")
		}
		return v, nil
	case map[string]interface{}:
		image, ok := v["image"].(string)
		if !ok || image == "" {
			return "", fmt.Errorf("container: is missing image:")
		}
		return image, nil
	}
	return "", fmt.Errorf("container: has an unsupported format")
}

func parseStep(jobName string, idx int, raw map[string]interface{}, ctx exprContext) (pipeline.Step, []pipeline.Finding, error) {
	name, _ := raw["name"].(string)

	if runVal, ok := raw["run"]; ok {
		rawCommand, ok := runVal.(string)
		if !ok || rawCommand == "" {
			return pipeline.Step{}, nil, fmt.Errorf("step %d: run: must be a non-empty string", idx)
		}
		if name == "" {
			name = fmt.Sprintf("run[%d]", idx)
		}
		shell, _ := raw["shell"].(string)
		workingDir, _ := raw["working-directory"].(string)

		var findings []pipeline.Finding
		command, fs := substituteExpressions(rawCommand, jobName, name, ctx)
		findings = append(findings, fs...)

		env := toStringMap(raw["env"])
		if env != nil {
			expandedEnv := make(map[string]string, len(env))
			for k, v := range env {
				sv, fs := substituteExpressions(v, jobName, name, ctx)
				expandedEnv[k] = sv
				findings = append(findings, fs...)
			}
			env = expandedEnv
		}

		step := pipeline.Step{
			Name:             name,
			Command:          command,
			Env:              env,
			Shell:            shell,
			WorkingDirectory: workingDir,
		}
		return step, findings, nil
	}

	if usesVal, ok := raw["uses"]; ok {
		action, _ := usesVal.(string)
		if action == "" {
			return pipeline.Step{}, nil, fmt.Errorf("step %d: uses: must be a non-empty string", idx)
		}
		step, finding := noOpStep(jobName, idx, action)
		return step, []pipeline.Finding{finding}, nil
	}

	return pipeline.Step{}, nil, fmt.Errorf("step %d: expected run: or uses:", idx)
}

// noOpStep turns an unexecuted `uses:` action into a visible, harmless log
// line rather than erroring or silently dropping it — see the "Known
// limitations" note in CLAUDE.md.
func noOpStep(jobName string, idx int, action string) (pipeline.Step, pipeline.Finding) {
	level := pipeline.Unsupported
	reason := fmt.Sprintf("uses: %s is not supported yet, skipping", action)
	for _, prefix := range noOpUsesPrefixes {
		if strings.HasPrefix(action, prefix) {
			level = pipeline.Emulated
			reason = fmt.Sprintf("uses: %s is a no-op: the repo is already mounted at /workspace", action)
			break
		}
	}
	name := fmt.Sprintf("uses[%d]", idx)
	step := pipeline.Step{
		Name:    name,
		Command: fmt.Sprintf("echo \"polyci: %s\"", reason),
	}
	finding := pipeline.Finding{
		Job:     jobName,
		Step:    name,
		Feature: action,
		Level:   level,
		Detail:  reason,
	}
	return step, finding
}

func decodeInterface(node *yaml.Node) interface{} {
	var v interface{}
	_ = node.Decode(&v)
	return v
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

// toStringSlice accepts either a single string or a list of strings, as
// GitHub Actions allows both for needs:.
func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
