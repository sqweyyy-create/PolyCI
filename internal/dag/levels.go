// Package dag resolves a named dependency graph into levels: each node's
// distance from having no unmet dependencies, and a flat execution order
// grouped by ascending level. It's shared by any provider whose jobs
// declare "run after" dependencies on sibling jobs (CircleCI's workflow
// `requires:`, GitHub Actions' job `needs:`), so the same algorithm and
// error handling (unknown dependency, circular dependency, duplicate node)
// isn't reimplemented per provider.
package dag

import "fmt"

// Node is one entry in a dependency graph: a name and the names of other
// nodes in the same graph it depends on.
type Node struct {
	Name    string
	Depends []string
}

// Levels assigns each node the length of its longest dependency chain (0
// for a node with no dependencies), so nodes at the same level can run one
// after another while still guaranteeing every dependency's level is lower
// than its dependents'. It also returns a flat execution order: nodes
// grouped by ascending level, stable within a level by the order given in
// nodes.
func Levels(nodes []Node) (levels map[string]int, order []string, err error) {
	index := map[string]int{}
	for i, n := range nodes {
		if _, dup := index[n.Name]; dup {
			return nil, nil, fmt.Errorf("%q appears more than once", n.Name)
		}
		index[n.Name] = i
	}

	levels = map[string]int{}
	visiting := map[string]bool{}

	var resolve func(name string) (int, error)
	resolve = func(name string) (int, error) {
		if lvl, ok := levels[name]; ok {
			return lvl, nil
		}
		i, ok := index[name]
		if !ok {
			return 0, fmt.Errorf("depends on unknown %q", name)
		}
		if visiting[name] {
			return 0, fmt.Errorf("circular dependency involving %q", name)
		}
		visiting[name] = true
		maxDep := -1
		for _, dep := range nodes[i].Depends {
			depLvl, err := resolve(dep)
			if err != nil {
				return 0, err
			}
			if depLvl > maxDep {
				maxDep = depLvl
			}
		}
		visiting[name] = false
		lvl := maxDep + 1
		levels[name] = lvl
		return lvl, nil
	}

	maxLevel := 0
	for _, n := range nodes {
		lvl, err := resolve(n.Name)
		if err != nil {
			return nil, nil, err
		}
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	for lvl := 0; lvl <= maxLevel; lvl++ {
		for _, n := range nodes {
			if levels[n.Name] == lvl {
				order = append(order, n.Name)
			}
		}
	}
	return levels, order, nil
}
