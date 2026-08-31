package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sqweyyy-create/PolyCI/internal/executor"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// printCheck writes a compatibility report for p to w: which jobs the
// parser could run at all (and, for anything it couldn't, why), an
// itemized list of every Emulated/Unsupported Finding it recorded, a
// per-category breakdown of how faithfully PolyCI handles the features the
// config actually uses, and an overall qualitative confidence label. It
// never touches Docker and never runs anything — it's purely a read of
// what the parser already determined while building p.
func printCheck(w io.Writer, file string, p *pipeline.Pipeline) error {
	totalSteps := 0
	for _, j := range p.Jobs {
		totalSteps += len(j.Steps)
	}

	fmt.Fprintf(w, "Compatibility check for %s (%d job(s) runnable, %d job(s) skipped, %d step(s)):\n",
		file, len(p.Jobs), len(p.SkippedJobs), totalSteps)

	if len(p.SkippedJobs) > 0 {
		fmt.Fprintln(w, "\nSkipped jobs (recognized in the config, but not executed at all):")
		for _, sj := range p.SkippedJobs {
			fmt.Fprintf(w, "  - %s: %s\n", sj.Name, sj.Reason)
		}
	}

	var emulated, unsupported []pipeline.Finding
	for _, f := range p.Findings {
		switch f.Level {
		case pipeline.Emulated:
			emulated = append(emulated, f)
		case pipeline.Unsupported:
			unsupported = append(unsupported, f)
		}
	}

	if len(emulated) > 0 {
		fmt.Fprintln(w, "\nEmulated (approximated, not a faithful implementation):")
		for _, f := range emulated {
			printFinding(w, f)
		}
	}
	if len(unsupported) > 0 {
		fmt.Fprintln(w, "\nUnsupported (recognized but not implemented — may change job behavior):")
		for _, f := range unsupported {
			printFinding(w, f)
		}
	}

	categories := categorize(p)
	fmt.Fprintln(w, "\nCategory breakdown:")
	for _, c := range categories {
		fmt.Fprintf(w, "  %-25s %s\n", c.name+":", c.status)
	}

	fmt.Fprintf(w, "\nOverall confidence: %s\n", confidenceLabel(categories))

	return nil
}

func printFinding(w io.Writer, f pipeline.Finding) {
	loc := f.Job
	if f.Step != "" {
		loc = fmt.Sprintf("%s/%s", f.Job, f.Step)
	}
	if loc == "" {
		loc = "(pipeline)"
	}
	fmt.Fprintf(w, "  - [%s] %s: %s — %s\n", loc, f.Feature, f.Level, f.Detail)
}

// categoryResult is one row of printCheck's category breakdown: how
// faithfully PolyCI handles one class of feature, given what this specific
// config actually uses.
type categoryResult struct {
	name   string
	status string // "Supported", "Emulated", "Unsupported", or "Not Present"
}

// categorize classifies p into a fixed set of feature categories. A single
// "Estimated fidelity: X%" number averages together things that aren't
// comparable — a config that never touches services and one whose services
// are fully broken shouldn't produce a similar-looking score to a config
// with one cosmetic emulation — so each category gets its own row instead:
// Not Present when the config never exercises it at all, otherwise the
// worst level any Finding in that category actually reached (Unsupported
// beats Emulated beats Supported).
func categorize(p *pipeline.Pipeline) []categoryResult {
	return []categoryResult{
		{"Environment", categoryEnvironment(p)},
		{"Shell/working-directory", categoryShellWorkingDir(p)},
		{"Filesystem/checkout", categoryFilesystem(p)},
		{"Services", categoryServices(p)},
		{"Expressions", categoryExpressions(p)},
	}
}

// categoryStatus applies the shared Not Present / Unsupported / Emulated /
// Supported priority every category below uses.
func categoryStatus(used, hasUnsupported, hasEmulated bool) string {
	switch {
	case !used:
		return "Not Present"
	case hasUnsupported:
		return "Unsupported"
	case hasEmulated:
		return "Emulated"
	default:
		return "Supported"
	}
}

// categoryEnvironment covers job/step environment variables. No parser
// ever records a Finding for these — env var handling is fully
// implemented everywhere it's used — so this is always Supported or Not
// Present, never Emulated/Unsupported.
func categoryEnvironment(p *pipeline.Pipeline) string {
	used := false
	for _, j := range p.Jobs {
		if len(j.Variables) > 0 {
			used = true
		}
		for _, s := range j.Steps {
			if len(s.Env) > 0 {
				used = true
			}
		}
	}
	return categoryStatus(used, false, false)
}

// categoryShellWorkingDir covers a step's shell:/working-directory: —
// classified directly from the parsed Step values (via
// executor.IsShellSupported) rather than from Findings, since an
// unsupported shell is currently a runtime executor error, not something
// any parser records as a Finding.
func categoryShellWorkingDir(p *pipeline.Pipeline) string {
	used := false
	unsupported := false
	for _, j := range p.Jobs {
		for _, s := range j.Steps {
			if s.Shell != "" || s.WorkingDirectory != "" {
				used = true
			}
			if s.Shell != "" && !executor.IsShellSupported(s.Shell) {
				unsupported = true
			}
		}
	}
	return categoryStatus(used, unsupported, false)
}

// categoryFilesystem covers workspace/checkout access. Every job that
// actually runs gets the real repository bind-mounted, so this is
// Supported by default; it downgrades to Emulated only when the config
// also used an explicit no-op checkout step (CircleCI's checkout,
// GitHub Actions' actions/checkout) — a real substitute, but still not the
// provider's real checkout behavior (e.g. a specific ref/depth).
func categoryFilesystem(p *pipeline.Pipeline) string {
	used := len(p.Jobs) > 0
	emulated := false
	for _, f := range p.Findings {
		if f.Level != pipeline.Emulated {
			continue
		}
		if f.Feature == "checkout" || strings.HasPrefix(f.Feature, "actions/checkout") {
			emulated = true
		}
	}
	return categoryStatus(used, false, emulated)
}

// categoryServices covers GitLab services:/CircleCI's extra docker:
// entries. No parser ever records a Finding for these — service
// containers are fully implemented — so this is always Supported or Not
// Present.
func categoryServices(p *pipeline.Pipeline) string {
	used := false
	for _, j := range p.Jobs {
		if len(j.Services) > 0 {
			used = true
		}
	}
	return categoryStatus(used, false, false)
}

// categoryExpressions covers GitHub Actions' ${{ }} syntax. Every
// expression — resolved or not — leaves a Finding behind (see
// internal/githubactions/expression.go's "supported" case), which is what
// lets this category tell "used and fully resolved" apart from "never used
// at all": a successfully-substituted expression's value looks identical
// to text that never had an expression in it in the first place.
func categoryExpressions(p *pipeline.Pipeline) string {
	used := false
	unsupported := false
	emulated := false
	for _, f := range p.Findings {
		if !strings.HasPrefix(f.Feature, "${{ ") {
			continue
		}
		used = true
		switch f.Level {
		case pipeline.Unsupported:
			unsupported = true
		case pipeline.Emulated:
			emulated = true
		}
	}
	return categoryStatus(used, unsupported, emulated)
}

// confidenceLabel reduces the category breakdown to one qualitative
// signal. Each category that's actually used (not "Not Present")
// contributes to a score — Unsupported counts double an Emulated, since a
// missing behavior is a bigger risk than an approximated one — and the
// total score buckets into HIGH (nothing degraded), MEDIUM (a little), or
// LOW (enough that the run should be expected to diverge from the real
// provider in a way that matters).
func confidenceLabel(categories []categoryResult) string {
	score := 0
	for _, c := range categories {
		switch c.status {
		case "Unsupported":
			score += 2
		case "Emulated":
			score += 1
		}
	}
	switch {
	case score == 0:
		return "HIGH"
	case score <= 2:
		return "MEDIUM"
	default:
		return "LOW"
	}
}
