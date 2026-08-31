package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// TestPrintCheckClassifiesFindings proves `polyci check`'s report
// correctly separates Supported/Emulated/Unsupported, lists every finding
// with its job/step/feature, and never touches Docker — this test never
// imports or calls anything from internal/executor, never constructs a
// Docker client, and would run identically with no Docker daemon present.
func TestPrintCheckClassifiesFindings(t *testing.T) {
	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:  "build",
				Stage: "build",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "uses[0]", Command: "echo checkout"},
					{Name: "run[1]", Command: "echo hi"},
				},
			},
			{
				Name:  "deploy",
				Stage: "deploy",
				Image: "alpine:3.19",
				Steps: []pipeline.Step{
					{Name: "run[0]", Command: "echo deploying"},
				},
			},
		},
		Findings: []pipeline.Finding{
			{Job: "build", Step: "uses[0]", Feature: "actions/checkout@v4", Level: pipeline.Emulated, Detail: "workspace already mounted"},
			{Job: "deploy", Feature: "extends:", Level: pipeline.Unsupported, Detail: "chain deeper than one level"},
		},
	}

	var buf bytes.Buffer
	if err := printCheck(&buf, "workflow.yml", p); err != nil {
		t.Fatalf("printCheck: %v", err)
	}
	out := buf.String()

	// 3 total steps + 1 extra unit (the job-level extends: finding) = 4
	// units; 2 findings downgrade 2 of them, leaving 2 Supported.
	if !strings.Contains(out, "Supported:   2") {
		t.Errorf("output missing Supported: 2: %q", out)
	}
	if !strings.Contains(out, "Emulated:    1") {
		t.Errorf("output missing Emulated: 1: %q", out)
	}
	if !strings.Contains(out, "Unsupported: 1") {
		t.Errorf("output missing Unsupported: 1: %q", out)
	}

	if !strings.Contains(out, "[build/uses[0]] actions/checkout@v4: Emulated") {
		t.Errorf("output missing itemized checkout finding: %q", out)
	}
	if !strings.Contains(out, "[deploy] extends:: Unsupported") {
		t.Errorf("output missing itemized extends: finding: %q", out)
	}

	if !strings.Contains(out, "Estimated fidelity:") {
		t.Errorf("output missing fidelity line: %q", out)
	}
}

// TestPrintCheckNoFindingsFullFidelity proves a pipeline with no findings
// at all reports 100% fidelity and no Emulated/Unsupported sections.
func TestPrintCheckNoFindingsFullFidelity(t *testing.T) {
	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{Name: "build", Stage: "build", Image: "alpine:3.19", Steps: []pipeline.Step{{Name: "s1", Command: "echo hi"}}},
		},
	}

	var buf bytes.Buffer
	if err := printCheck(&buf, "config.yml", p); err != nil {
		t.Fatalf("printCheck: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Supported:   1") {
		t.Errorf("output missing Supported: 1: %q", out)
	}
	if !strings.Contains(out, "Emulated:    0") || !strings.Contains(out, "Unsupported: 0") {
		t.Errorf("output should show 0 Emulated and 0 Unsupported: %q", out)
	}
	if strings.Contains(out, "\nEmulated (") || strings.Contains(out, "\nUnsupported (") {
		t.Errorf("output should not print empty Emulated/Unsupported sections: %q", out)
	}
	if !strings.Contains(out, "Estimated fidelity: 100%") {
		t.Errorf("output missing 100%% fidelity: %q", out)
	}
}
