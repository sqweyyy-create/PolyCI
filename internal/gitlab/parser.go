// Package gitlab parses .gitlab-ci.yml files into the provider-agnostic
// pipeline model defined in internal/pipeline.
package gitlab

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// defaultStages is the stage order GitLab uses when a config omits the
// top-level `stages:` key.
var defaultStages = []string{".pre", "build", "test", "deploy", ".post"}

// reservedTopLevelKeys are the config keywords GitLab actually recognizes
// at the top level of a .gitlab-ci.yml — as opposed to keywords that only
// exist at the job level (tags:, retry:, timeout:, interruptible:,
// secrets:, parallel:, trigger:, inherit: are all job-only; their global
// defaults live under default:, not as bare top-level keys). This list
// must stay conservative: any name in it can never be used as a job name,
// since it silently drops that job with no error — see the `pages`
// regression this list previously caused (a job named exactly `pages`,
// which is an entirely ordinary job name in real GitLab CI, was being
// treated as a reserved keyword and dropped without a trace).
var reservedTopLevelKeys = map[string]bool{
	"stages":        true,
	"variables":     true,
	"default":       true,
	"workflow":      true,
	"include":       true,
	"image":         true,
	"before_script": true,
	"after_script":  true,
	"cache":         true,
	"services":      true,
}

// Parse converts raw .gitlab-ci.yml bytes into a provider-agnostic
// pipeline.Pipeline.
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

	p := &pipeline.Pipeline{Stages: append([]string{}, defaultStages...)}

	globalVariables := map[string]string{}
	var defaultImage, topLevelImage string
	var defaultBefore, defaultAfter, topBefore, topAfter []string
	var topServices []pipeline.Service

	// First pass: pick up stage list and defaults, which job resolution
	// depends on, regardless of where they appear in the file.
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]

		switch key {
		case "stages":
			var stages []string
			if err := val.Decode(&stages); err != nil {
				return nil, fmt.Errorf("stages: %w", err)
			}
			p.Stages = stages
		case "variables":
			vars, err := decodeStringMap(val)
			if err != nil {
				return nil, fmt.Errorf("variables: %w", err)
			}
			globalVariables = vars
		case "default":
			def, err := decodeMap(val)
			if err != nil {
				return nil, fmt.Errorf("default: %w", err)
			}
			if img, ok := def["image"]; ok {
				defaultImage = imageName(img)
			}
			if bs, ok := def["before_script"]; ok {
				defaultBefore = toStringSlice(bs)
			}
			if as, ok := def["after_script"]; ok {
				defaultAfter = toStringSlice(as)
			}
		case "image":
			topLevelImage = imageName(nodeToInterface(val))
		case "before_script":
			topBefore = toStringSlice(nodeToInterface(val))
		case "after_script":
			topAfter = toStringSlice(nodeToInterface(val))
		case "services":
			services, err := parseServices(nodeToInterface(val))
			if err != nil {
				return nil, fmt.Errorf("services: %w", err)
			}
			topServices = services
		case "include":
			p.Findings = append(p.Findings, pipeline.Finding{
				Feature: "include:",
				Level:   pipeline.Unsupported,
				Detail:  "included files are not fetched or expanded; only jobs defined directly in this file are considered",
			})
		}
	}

	// Preliminary pass: decode every top-level entry that isn't a reserved
	// keyword — hidden job templates (name starting with '.') included —
	// so extends: can look up any of them by name below. Hidden templates
	// are never themselves turned into runnable jobs (see the second pass),
	// but a real job's extends: (or a template it's built from via a YAML
	// anchor merge) can reference one.
	allEntries := map[string]map[string]interface{}{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if reservedTopLevelKeys[key] || val.Kind != yaml.MappingNode {
			continue
		}
		raw, err := decodeMap(val)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", key, err)
		}
		allEntries[key] = raw
	}

	// Second pass: job definitions, in file order.
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]

		if reservedTopLevelKeys[key] {
			continue
		}
		if len(key) > 0 && key[0] == '.' {
			// Hidden job / template — not runnable directly.
			continue
		}
		if val.Kind != yaml.MappingNode {
			continue
		}

		raw, extendsFinding := resolveExtends(key, allEntries[key], allEntries)
		if extendsFinding != nil {
			p.Findings = append(p.Findings, *extendsFinding)
		}

		stage := "test"
		if s, ok := raw["stage"].(string); ok && s != "" {
			stage = s
		}
		if !contains(p.Stages, stage) {
			return nil, fmt.Errorf("job %q: stage %q is not declared in stages", key, stage)
		}

		image := defaultImage
		if topLevelImage != "" {
			image = topLevelImage
		}
		if img, ok := raw["image"]; ok {
			image = imageName(img)
		}
		if image == "" {
			return nil, fmt.Errorf("job %q: no image specified (set image:, default.image, or top-level image:)", key)
		}

		script := toStringSlice(raw["script"])
		if len(script) == 0 {
			return nil, fmt.Errorf("job %q: script is required and must not be empty", key)
		}

		before := defaultBefore
		if topBefore != nil {
			before = topBefore
		}
		if bs, ok := raw["before_script"]; ok {
			before = toStringSlice(bs)
		}

		after := defaultAfter
		if topAfter != nil {
			after = topAfter
		}
		if as, ok := raw["after_script"]; ok {
			after = toStringSlice(as)
		}

		jobVars := map[string]string{}
		for k, v := range globalVariables {
			jobVars[k] = v
		}
		if vm, ok := raw["variables"]; ok {
			for k, v := range toStringMap(vm) {
				jobVars[k] = v
			}
		}

		services := topServices
		if sv, ok := raw["services"]; ok {
			svcs, err := parseServices(sv)
			if err != nil {
				return nil, fmt.Errorf("job %q: services: %w", key, err)
			}
			services = svcs
		}

		var steps []pipeline.Step
		for idx, cmd := range before {
			steps = append(steps, pipeline.Step{Name: fmt.Sprintf("before_script[%d]", idx), Command: cmd, Phase: pipeline.PhaseMain})
		}
		for idx, cmd := range script {
			steps = append(steps, pipeline.Step{Name: fmt.Sprintf("script[%d]", idx), Command: cmd, Phase: pipeline.PhaseMain})
		}
		for idx, cmd := range after {
			steps = append(steps, pipeline.Step{Name: fmt.Sprintf("after_script[%d]", idx), Command: cmd, Phase: pipeline.PhaseAfter})
		}

		p.Jobs = append(p.Jobs, pipeline.Job{
			Name:      key,
			Stage:     stage,
			Image:     image,
			Variables: jobVars,
			Steps:     steps,
			Services:  services,
		})
	}

	if len(p.Jobs) == 0 {
		return nil, fmt.Errorf("no runnable jobs found in config")
	}

	assignDependsOn(p)

	return p, nil
}

// assignDependsOn reproduces GitLab's stage-barrier semantics as explicit
// per-job dependencies: every job depends on every job in the nearest
// non-empty preceding stage (skipping stages with no jobs in them, exactly
// as GitLab does), so the executor's dependency-driven scheduler runs
// stages in order while still running same-stage jobs concurrently.
func assignDependsOn(p *pipeline.Pipeline) {
	stageIndex := make(map[string]int, len(p.Stages))
	for i, s := range p.Stages {
		stageIndex[s] = i
	}

	jobNamesByStage := map[int][]string{}
	for _, j := range p.Jobs {
		idx := stageIndex[j.Stage]
		jobNamesByStage[idx] = append(jobNamesByStage[idx], j.Name)
	}

	for i := range p.Jobs {
		idx := stageIndex[p.Jobs[i].Stage]
		for prev := idx - 1; prev >= 0; prev-- {
			if names, ok := jobNamesByStage[prev]; ok && len(names) > 0 {
				p.Jobs[i].DependsOn = names
				break
			}
		}
	}
}

// decodeMap decodes a yaml mapping node into a map[string]interface{}.
func decodeMap(node *yaml.Node) (map[string]interface{}, error) {
	m := map[string]interface{}{}
	if err := node.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// decodeStringMap decodes a yaml mapping node into a map[string]string,
// stringifying scalar values (GitLab variables are often numbers/bools).
func decodeStringMap(node *yaml.Node) (map[string]string, error) {
	raw, err := decodeMap(node)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out, nil
}

// nodeToInterface decodes an arbitrary yaml node into interface{}.
func nodeToInterface(node *yaml.Node) interface{} {
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

// imageName extracts an image reference from either a bare string or a
// GitLab `image: {name: ...}` mapping.
func imageName(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]interface{}:
		if name, ok := t["name"].(string); ok {
			return name
		}
	}
	return ""
}

// toStringSlice accepts either a single string or a list of strings, as
// GitLab allows both for script/before_script/after_script.
func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprintf("%v", item))
		}
		return out
	}
	return nil
}

// parseServices converts an already-decoded `services:` value (a list of
// either bare image strings, or maps with name:/alias:/variables:) into
// pipeline.Services. Each entry's alias defaults to
// pipeline.DefaultServiceAlias(image) when not given explicitly.
func parseServices(v interface{}) ([]pipeline.Service, error) {
	list, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("must be a list")
	}
	var services []pipeline.Service
	for i, item := range list {
		switch t := item.(type) {
		case string:
			services = append(services, pipeline.Service{Image: t, Alias: pipeline.DefaultServiceAlias(t)})
		case map[string]interface{}:
			img, _ := t["name"].(string)
			if img == "" {
				return nil, fmt.Errorf("[%d]: missing name", i)
			}
			alias, _ := t["alias"].(string)
			if alias == "" {
				alias = pipeline.DefaultServiceAlias(img)
			}
			services = append(services, pipeline.Service{
				Image:     img,
				Alias:     alias,
				Variables: toStringMap(t["variables"]),
			})
		default:
			return nil, fmt.Errorf("[%d]: unsupported format", i)
		}
	}
	return services, nil
}

// resolveExtends applies GitLab's extends: for a single level: the
// referenced job's (or jobs', if a list) own fields become defaults for
// this job, with this job's own fields taking precedence — variables: is
// deep-merged (child's keys win per-key), everything else is a shallow
// override, matching GitLab's own extends merge semantics. Only one
// level is resolved: if a referenced job itself also has an unresolved
// extends:, that isn't followed further, and the returned Finding notes
// it explicitly rather than silently dropping it. Returns raw unchanged
// (and a nil Finding) when there's no extends: at all.
func resolveExtends(jobName string, raw map[string]interface{}, allEntries map[string]map[string]interface{}) (map[string]interface{}, *pipeline.Finding) {
	extendsRaw, ok := raw["extends"]
	if !ok {
		return raw, nil
	}

	var parents []string
	switch v := extendsRaw.(type) {
	case string:
		parents = []string{v}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				parents = append(parents, s)
			}
		}
	}

	merged := raw
	var missing, deep []string
	for _, parentName := range parents {
		parent, ok := allEntries[parentName]
		if !ok {
			missing = append(missing, parentName)
			continue
		}
		merged = mergeExtends(merged, parent)
		if _, stillExtends := parent["extends"]; stillExtends {
			deep = append(deep, parentName)
		}
	}

	if len(missing) > 0 {
		return merged, &pipeline.Finding{
			Job:     jobName,
			Feature: "extends:",
			Level:   pipeline.Unsupported,
			Detail:  fmt.Sprintf("references unknown job(s) %v", missing),
		}
	}
	if len(deep) > 0 {
		return merged, &pipeline.Finding{
			Job:     jobName,
			Feature: "extends:",
			Level:   pipeline.Unsupported,
			Detail:  fmt.Sprintf("only a single level of extends: is resolved; %v itself uses extends:, which is not followed", deep),
		}
	}
	return merged, &pipeline.Finding{
		Job:     jobName,
		Feature: "extends:",
		Level:   pipeline.Emulated,
		Detail:  fmt.Sprintf("merged in %v (single level only — a chain of extends: beyond that isn't followed)", parents),
	}
}

// mergeExtends layers child's own fields over parent's: variables: is
// deep-merged (child's individual keys override parent's, the rest is
// unioned), everything else is a shallow override — child wins outright
// if it defines the key at all, otherwise parent's value is inherited.
func mergeExtends(child, parent map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(parent)+len(child))
	for k, v := range parent {
		merged[k] = v
	}
	for k, v := range child {
		if k == "variables" {
			pv, _ := merged["variables"].(map[string]interface{})
			cv, _ := v.(map[string]interface{})
			out := make(map[string]interface{}, len(pv)+len(cv))
			for kk, vv := range pv {
				out[kk] = vv
			}
			for kk, vv := range cv {
				out[kk] = vv
			}
			merged["variables"] = out
		} else {
			merged[k] = v
		}
	}
	return merged
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
