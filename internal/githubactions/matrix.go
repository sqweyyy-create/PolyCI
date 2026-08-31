package githubactions

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// matrixCombinations returns every combination of a job's strategy.matrix
// axes — the cartesian product of each list-valued matrix key — in a
// deterministic order (matrix keys sorted alphabetically; each axis's own
// values in the order they're written). A job with no strategy.matrix at
// all returns a single nil combination, meaning "no matrix substitution"
// rather than "one axis with no values". matrix.include:/matrix.exclude:
// entries aren't applied — a Finding says so explicitly rather than
// silently ignoring them.
func matrixCombinations(jobName string, raw map[string]interface{}) (combos []map[string]string, hasMatrix bool, findings []pipeline.Finding) {
	strategy, ok := raw["strategy"].(map[string]interface{})
	if !ok {
		return []map[string]string{nil}, false, nil
	}
	matrix, ok := strategy["matrix"].(map[string]interface{})
	if !ok {
		return []map[string]string{nil}, false, nil
	}

	keys := make([]string, 0, len(matrix))
	for k := range matrix {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type axis struct {
		key    string
		values []string
	}
	var axes []axis
	for _, k := range keys {
		if k == "include" || k == "exclude" {
			findings = append(findings, pipeline.Finding{
				Job:     jobName,
				Feature: fmt.Sprintf("strategy.matrix.%s:", k),
				Level:   pipeline.Unsupported,
				Detail:  "not applied; only the cartesian product of this job's list-valued matrix keys is expanded",
			})
			continue
		}
		list, ok := matrix[k].([]interface{})
		if !ok {
			continue
		}
		values := make([]string, len(list))
		for i, v := range list {
			values[i] = fmt.Sprintf("%v", v)
		}
		axes = append(axes, axis{key: k, values: values})
	}

	if len(axes) == 0 {
		return []map[string]string{nil}, true, findings
	}

	combos = []map[string]string{{}}
	for _, ax := range axes {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range ax.values {
				nc := make(map[string]string, len(c)+1)
				for kk, vv := range c {
					nc[kk] = vv
				}
				nc[ax.key] = v
				next = append(next, nc)
			}
		}
		combos = next
	}
	return combos, true, findings
}

// comboLabel names a matrix combination the way it's appended to the job
// name, e.g. "node=18" or "node=18, os=ubuntu-latest" — deterministic
// (matrix keys sorted) since a Go map has no defined iteration order.
func comboLabel(combo map[string]string) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + combo[k]
	}
	return strings.Join(parts, ", ")
}
