package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// TestPrintPlanDiamondDependency proves `polyci plan`'s output correctly
// shows a diamond dependency's parallel/sequential structure — A -> B,
// A -> C, B and C -> D — without touching Docker at all: this test never
// imports or calls anything from internal/executor, never constructs a
// Docker client, and would run identically with no Docker daemon present.
func TestPrintPlanDiamondDependency(t *testing.T) {
	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{Name: "A", Stage: "build", Image: "alpine:3.19"},
			{Name: "B", Stage: "test", Image: "alpine:3.19", DependsOn: []string{"A"}},
			{Name: "C", Stage: "test", Image: "alpine:3.19", DependsOn: []string{"A"}},
			{Name: "D", Stage: "deploy", Image: "alpine:3.19", DependsOn: []string{"B", "C"}},
		},
	}

	var buf bytes.Buffer
	if err := printPlan(&buf, "diamond.yml", p); err != nil {
		t.Fatalf("printPlan: %v", err)
	}
	out := buf.String()

	lines := strings.Split(out, "\n")
	var level0, level1, level2 string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "Level 0"):
			level0 = line
		case strings.HasPrefix(line, "Level 1"):
			level1 = line
		case strings.HasPrefix(line, "Level 2"):
			level2 = line
		}
	}

	if level0 == "" || !strings.Contains(level0, "A") {
		t.Errorf("Level 0 line missing or doesn't mention A: %q (full output: %q)", level0, out)
	}
	if strings.Contains(level0, "parallel") {
		t.Errorf("Level 0 has only one job (A) and should not be marked parallel: %q", level0)
	}

	if level1 == "" || !strings.Contains(level1, "parallel") {
		t.Errorf("Level 1 (B and C) should be marked parallel: %q (full output: %q)", level1, out)
	}
	if !strings.Contains(level1, "B") || !strings.Contains(level1, "C") {
		t.Errorf("Level 1 should mention both B and C: %q", level1)
	}

	if level2 == "" || !strings.Contains(level2, "D") {
		t.Errorf("Level 2 line missing or doesn't mention D: %q (full output: %q)", level2, out)
	}
	if strings.Contains(level2, "parallel") {
		t.Errorf("Level 2 has only one job (D) and should not be marked parallel: %q", level2)
	}

	if !strings.Contains(out, "depends on: A") {
		t.Errorf("output missing B/C's dependency on A: %q", out)
	}
	if !strings.Contains(out, "depends on: B, C") {
		t.Errorf("output missing D's dependency on B and C: %q", out)
	}
	if !strings.Contains(out, "depends on: (none)") {
		t.Errorf("output missing A's lack of dependencies: %q", out)
	}
}
