package dag

import "testing"

func TestLevelsFanOutFanIn(t *testing.T) {
	// build -> {test, lint} -> deploy
	nodes := []Node{
		{Name: "build"},
		{Name: "test", Depends: []string{"build"}},
		{Name: "lint", Depends: []string{"build"}},
		{Name: "deploy", Depends: []string{"test", "lint"}},
	}
	levels, order, err := Levels(nodes)
	if err != nil {
		t.Fatalf("Levels: %v", err)
	}
	want := map[string]int{"build": 0, "test": 1, "lint": 1, "deploy": 2}
	for name, lvl := range want {
		if levels[name] != lvl {
			t.Errorf("levels[%q] = %d, want %d", name, levels[name], lvl)
		}
	}
	wantOrder := []string{"build", "test", "lint", "deploy"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Errorf("order[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestLevelsCircularDependency(t *testing.T) {
	nodes := []Node{
		{Name: "a", Depends: []string{"b"}},
		{Name: "b", Depends: []string{"a"}},
	}
	if _, _, err := Levels(nodes); err == nil {
		t.Fatal("expected error for circular dependency, got nil")
	}
}

func TestLevelsUnknownDependency(t *testing.T) {
	nodes := []Node{{Name: "a", Depends: []string{"missing"}}}
	if _, _, err := Levels(nodes); err == nil {
		t.Fatal("expected error for unknown dependency, got nil")
	}
}

func TestLevelsDuplicateNode(t *testing.T) {
	nodes := []Node{{Name: "a"}, {Name: "a"}}
	if _, _, err := Levels(nodes); err == nil {
		t.Fatal("expected error for duplicate node, got nil")
	}
}

func TestFilterSkippedRemovesOnlySkippedNode(t *testing.T) {
	// build -> test; lint is independent of both.
	nodes := []Node{
		{Name: "build"},
		{Name: "test", Depends: []string{"build"}},
		{Name: "lint"},
	}
	kept, reasons := FilterSkipped(nodes, map[string]string{"test": "no container: specified"})

	if len(kept) != 2 {
		t.Fatalf("kept = %+v, want 2 nodes (build, lint)", kept)
	}
	names := map[string]bool{}
	for _, n := range kept {
		names[n.Name] = true
	}
	if !names["build"] || !names["lint"] {
		t.Errorf("kept = %+v, want build and lint present", kept)
	}
	if names["test"] {
		t.Errorf("kept = %+v, want test removed (it was skipped)", kept)
	}
	if reasons["test"] != "no container: specified" {
		t.Errorf(`reasons["test"] = %q, want the original reason unchanged`, reasons["test"])
	}
}

func TestFilterSkippedCascadesThroughDependencyChain(t *testing.T) {
	// build -> test -> deploy; deploy depends on test which depends on the
	// skipped build, so all three of build/test/deploy should end up
	// skipped even though only "build" was skipped directly. "lint" has no
	// relationship to any of them and must survive untouched.
	nodes := []Node{
		{Name: "build"},
		{Name: "test", Depends: []string{"build"}},
		{Name: "deploy", Depends: []string{"test"}},
		{Name: "lint"},
	}
	kept, reasons := FilterSkipped(nodes, map[string]string{"build": "no container: specified"})

	if len(kept) != 1 || kept[0].Name != "lint" {
		t.Fatalf("kept = %+v, want only lint", kept)
	}
	if reasons["build"] != "no container: specified" {
		t.Errorf(`reasons["build"] = %q, want the original reason unchanged`, reasons["build"])
	}
	if reasons["test"] == "" || reasons["test"] == reasons["build"] {
		t.Errorf(`reasons["test"] = %q, want a cascade reason distinct from build's own reason`, reasons["test"])
	}
	if reasons["deploy"] == "" {
		t.Errorf(`reasons["deploy"] = %q, want a cascade reason (deploy depends on test, which depends on the skipped build)`, reasons["deploy"])
	}
}
