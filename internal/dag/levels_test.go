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
