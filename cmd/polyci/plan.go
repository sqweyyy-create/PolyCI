package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sqweyyy-create/PolyCI/internal/dag"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// printPlan writes the resolved pipeline's structure to w: every job
// grouped by dependency level, each job's DependsOn, and which jobs would
// run in parallel (same level) versus sequentially (different levels) —
// using the same internal/dag algorithm the CircleCI and GitHub Actions
// parsers use to resolve requires:/needs: into levels, applied here to
// every provider's DependsOn edges. It never touches Docker.
func printPlan(w io.Writer, file string, p *pipeline.Pipeline) error {
	nodes := make([]dag.Node, len(p.Jobs))
	for i, j := range p.Jobs {
		nodes[i] = dag.Node{Name: j.Name, Depends: j.DependsOn}
	}
	levels, order, err := dag.Levels(nodes)
	if err != nil {
		return fmt.Errorf("resolving job dependencies: %w", err)
	}

	byName := make(map[string]pipeline.Job, len(p.Jobs))
	for _, j := range p.Jobs {
		byName[j.Name] = j
	}

	fmt.Fprintf(w, "Plan for %s (%d job(s)):\n\n", file, len(p.Jobs))

	maxLevel := 0
	for _, lvl := range levels {
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	for lvl := 0; lvl <= maxLevel; lvl++ {
		var names []string
		for _, name := range order {
			if levels[name] == lvl {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			continue
		}

		if len(names) > 1 {
			fmt.Fprintf(w, "Level %d (parallel): %s\n", lvl, strings.Join(names, ", "))
		} else {
			fmt.Fprintf(w, "Level %d: %s\n", lvl, names[0])
		}
		for _, name := range names {
			job := byName[name]
			deps := "(none)"
			if len(job.DependsOn) > 0 {
				deps = strings.Join(job.DependsOn, ", ")
			}
			fmt.Fprintf(w, "  - %s [stage=%s image=%s] depends on: %s\n", job.Name, job.Stage, job.Image, deps)
		}
	}

	if maxLevel > 0 {
		fmt.Fprintln(w, "\nLevels run one after another; jobs within the same level run in parallel.")
	}

	return nil
}
