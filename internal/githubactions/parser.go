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
// provider-agnostic pipeline.Pipeline. Job `needs:` dependencies are
// resolved into dependency levels, which become the pipeline's stages so
// independent jobs at the same level run before anything that depends on
// them.
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

	var nodes []dag.Node
	defs := map[string]jobDef{}
	for i := 0; i+1 < len(jobsNode.Content); i += 2 {
		name := jobsNode.Content[i].Value
		def, needs, err := parseJob(jobsNode.Content[i+1], globalEnv)
		if err != nil {
			return nil, fmt.Errorf("job %q: %w", name, err)
		}
		defs[name] = def
		nodes = append(nodes, dag.Node{Name: name, Depends: needs})
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
		})
	}

	if len(p.Jobs) == 0 {
		return nil, fmt.Errorf("no runnable jobs found in config")
	}
	return p, nil
}

type jobDef struct {
	image string
	env   map[string]string
	steps []pipeline.Step
}

func parseJob(node *yaml.Node, globalEnv map[string]string) (jobDef, []string, error) {
	raw, ok := decodeInterface(node).(map[string]interface{})
	if !ok {
		return jobDef{}, nil, fmt.Errorf("must be a mapping")
	}

	image, err := jobImage(raw)
	if err != nil {
		return jobDef{}, nil, err
	}

	env := map[string]string{}
	for k, v := range globalEnv {
		env[k] = v
	}
	for k, v := range toStringMap(raw["env"]) {
		env[k] = v
	}
	if c, ok := raw["container"].(map[string]interface{}); ok {
		for k, v := range toStringMap(c["env"]) {
			env[k] = v
		}
	}

	stepsRaw, ok := raw["steps"]
	if !ok {
		return jobDef{}, nil, fmt.Errorf("no steps defined")
	}
	stepsList, ok := stepsRaw.([]interface{})
	if !ok {
		return jobDef{}, nil, fmt.Errorf("steps must be a list")
	}

	var steps []pipeline.Step
	for idx, item := range stepsList {
		stepRaw, ok := item.(map[string]interface{})
		if !ok {
			return jobDef{}, nil, fmt.Errorf("step %d: must be a mapping", idx)
		}
		step, err := parseStep(idx, stepRaw)
		if err != nil {
			return jobDef{}, nil, err
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return jobDef{}, nil, fmt.Errorf("steps must not be empty")
	}

	return jobDef{image: image, env: env, steps: steps}, toStringSlice(raw["needs"]), nil
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

func parseStep(idx int, raw map[string]interface{}) (pipeline.Step, error) {
	name, _ := raw["name"].(string)

	if runVal, ok := raw["run"]; ok {
		command, ok := runVal.(string)
		if !ok || command == "" {
			return pipeline.Step{}, fmt.Errorf("step %d: run: must be a non-empty string", idx)
		}
		if name == "" {
			name = fmt.Sprintf("run[%d]", idx)
		}
		return pipeline.Step{Name: name, Command: command, Env: toStringMap(raw["env"])}, nil
	}

	if usesVal, ok := raw["uses"]; ok {
		action, _ := usesVal.(string)
		if action == "" {
			return pipeline.Step{}, fmt.Errorf("step %d: uses: must be a non-empty string", idx)
		}
		return noOpStep(idx, action), nil
	}

	return pipeline.Step{}, fmt.Errorf("step %d: expected run: or uses:", idx)
}

// noOpStep turns an unexecuted `uses:` action into a visible, harmless log
// line rather than erroring or silently dropping it — see the "Known
// limitations" note in CLAUDE.md.
func noOpStep(idx int, action string) pipeline.Step {
	reason := fmt.Sprintf("uses: %s is not supported yet, skipping", action)
	for _, prefix := range noOpUsesPrefixes {
		if strings.HasPrefix(action, prefix) {
			reason = fmt.Sprintf("uses: %s is a no-op: the repo is already mounted at /workspace", action)
			break
		}
	}
	return pipeline.Step{
		Name:    fmt.Sprintf("uses[%d]", idx),
		Command: fmt.Sprintf("echo \"polyci: %s\"", reason),
	}
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
