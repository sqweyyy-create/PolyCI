package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// categoryLineStatus finds the category-breakdown line for name (e.g.
// "Environment") in printCheck's output and returns its status word
// ("Supported", "Emulated", "Unsupported", "Not Present"), ignoring the
// exact column alignment printCheck happens to use.
func categoryLineStatus(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, name+":") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(trimmed, name+":"))
	}
	t.Fatalf("no category breakdown line found for %q in output:\n%s", name, out)
	return ""
}

// TestPrintCheckListsFindingsAndSkippedJobs proves `polyci check`'s report
// lists every Emulated/Unsupported finding with its job/step/feature, and
// clearly reports skipped jobs and why — never touches Docker, so this
// test would run identically with no Docker daemon present.
func TestPrintCheckListsFindingsAndSkippedJobs(t *testing.T) {
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
		},
		Findings: []pipeline.Finding{
			{Job: "build", Step: "uses[0]", Feature: "actions/checkout@v4", Level: pipeline.Emulated, Detail: "workspace already mounted"},
			{Job: "deploy", Feature: "extends:", Level: pipeline.Unsupported, Detail: "chain deeper than one level"},
		},
		SkippedJobs: []pipeline.SkippedJob{
			{Name: "legacy-deploy", Reason: "no container: specified"},
		},
	}

	var buf bytes.Buffer
	if err := printCheck(&buf, "workflow.yml", p); err != nil {
		t.Fatalf("printCheck: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "1 job(s) runnable, 1 job(s) skipped") {
		t.Errorf("output missing runnable/skipped job counts: %q", out)
	}
	if !strings.Contains(out, "legacy-deploy: no container: specified") {
		t.Errorf("output missing itemized skipped job: %q", out)
	}
	if !strings.Contains(out, "[build/uses[0]] actions/checkout@v4: Emulated") {
		t.Errorf("output missing itemized checkout finding: %q", out)
	}
	if !strings.Contains(out, "[deploy] extends:: Unsupported") {
		t.Errorf("output missing itemized extends: finding: %q", out)
	}
	if strings.Contains(out, "Estimated fidelity") {
		t.Errorf("output should no longer print a single fidelity percentage: %q", out)
	}
	if !strings.Contains(out, "Overall confidence:") {
		t.Errorf("output missing an overall confidence label: %q", out)
	}
}

// TestPrintCheckCategoryBreakdownMixedConfig proves the category breakdown
// correctly classifies a config that mixes Supported, Emulated, and
// Unsupported features across categories — the scenario a single
// "Estimated fidelity: X%" number would previously have averaged into one
// misleading figure.
func TestPrintCheckCategoryBreakdownMixedConfig(t *testing.T) {
	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:      "build",
				Image:     "alpine:3.19",
				Variables: map[string]string{"GREETING": "hi"}, // Environment: used, no findings -> Supported
				Services: []pipeline.Service{ // Services: used, no findings -> Supported
					{Image: "postgres:16-alpine", Alias: "postgres"},
				},
				Steps: []pipeline.Step{
					// Filesystem/checkout: used + Emulated finding -> Emulated
					{Name: "uses[0]", Command: "echo checkout"},
					// Shell/working-directory: used + unsupported shell -> Unsupported
					{Name: "run[1]", Command: "Write-Host hi", Shell: "pwsh"},
					// Expressions: used + mix of Supported and Unsupported findings -> Unsupported
					{Name: "run[2]", Command: "echo hi"},
				},
			},
		},
		Findings: []pipeline.Finding{
			{Job: "build", Step: "uses[0]", Feature: "actions/checkout@v4", Level: pipeline.Emulated, Detail: "workspace already mounted"},
			{Job: "build", Step: "run[2]", Feature: "${{ matrix.node }}", Level: pipeline.Supported, Detail: "substituted from this job's strategy.matrix"},
			{Job: "build", Step: "run[2]", Feature: "${{ secrets.TOKEN }}", Level: pipeline.Unsupported, Detail: "expression syntax is not evaluated"},
		},
	}

	var buf bytes.Buffer
	if err := printCheck(&buf, "ci.yml", p); err != nil {
		t.Fatalf("printCheck: %v", err)
	}
	out := buf.String()

	wantStatus := map[string]string{
		"Environment":             "Supported",
		"Shell/working-directory": "Unsupported",
		"Filesystem/checkout":     "Emulated",
		"Services":                "Supported",
		"Expressions":             "Unsupported",
	}
	for name, want := range wantStatus {
		if got := categoryLineStatus(t, out, name); got != want {
			t.Errorf("category %q = %q, want %q", name, got, want)
		}
	}

	// Two Unsupported categories (Shell/working-directory, Expressions) is
	// enough to drop confidence to LOW under the documented scoring.
	if !strings.Contains(out, "Overall confidence: LOW") {
		t.Errorf("output missing LOW confidence given 2 Unsupported categories: %q", out)
	}
}

// TestPrintCheckAllSupportedIsHighConfidence proves a config that uses
// several categories, all of them fully supported, reports HIGH
// confidence and every used category as Supported.
func TestPrintCheckAllSupportedIsHighConfidence(t *testing.T) {
	p := &pipeline.Pipeline{
		Jobs: []pipeline.Job{
			{
				Name:      "build",
				Image:     "alpine:3.19",
				Variables: map[string]string{"GREETING": "hi"},
				Services:  []pipeline.Service{{Image: "postgres:16-alpine", Alias: "postgres"}},
				Steps: []pipeline.Step{
					{Name: "run[0]", Command: "echo hi", Shell: "bash", WorkingDirectory: "subdir"},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := printCheck(&buf, "ci.yml", p); err != nil {
		t.Fatalf("printCheck: %v", err)
	}
	out := buf.String()

	wantStatus := map[string]string{
		"Environment":             "Supported",
		"Shell/working-directory": "Supported",
		"Services":                "Supported",
		"Expressions":             "Not Present",
	}
	for name, want := range wantStatus {
		if got := categoryLineStatus(t, out, name); got != want {
			t.Errorf("category %q = %q, want %q", name, got, want)
		}
	}
	if !strings.Contains(out, "Overall confidence: HIGH") {
		t.Errorf("output missing HIGH confidence: %q", out)
	}
}
