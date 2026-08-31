package main

import (
	"fmt"
	"io"

	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
)

// printCheck writes a compatibility report for p to w: how many of its
// jobs/steps PolyCI fully supports versus emulates versus doesn't support
// at all, an itemized list of every Emulated/Unsupported Finding the parser
// recorded, and a rough estimated-fidelity percentage. It never touches
// Docker and never runs anything — it's purely a read of what the parser
// already determined while building p.
func printCheck(w io.Writer, file string, p *pipeline.Pipeline) error {
	totalSteps := 0
	for _, j := range p.Jobs {
		totalSteps += len(j.Steps)
	}

	// Every step starts out Supported. A step-level Finding downgrades
	// exactly the step it's attached to. A job- or pipeline-level Finding
	// (extends:, include:, strategy.matrix: — Step == "") has no step of
	// its own to downgrade, so it counts as one extra unit added to the
	// total, itself downgraded from Supported.
	var emulated, unsupported []pipeline.Finding
	extraUnits := 0
	for _, f := range p.Findings {
		if f.Step == "" {
			extraUnits++
		}
		switch f.Level {
		case pipeline.Emulated:
			emulated = append(emulated, f)
		case pipeline.Unsupported:
			unsupported = append(unsupported, f)
		}
	}

	totalUnits := totalSteps + extraUnits
	supportedUnits := totalUnits - len(p.Findings)
	if supportedUnits < 0 {
		supportedUnits = 0
	}

	fmt.Fprintf(w, "Compatibility check for %s (%d job(s), %d step(s)):\n\n", file, len(p.Jobs), totalSteps)
	fmt.Fprintf(w, "  Supported:   %d\n", supportedUnits)
	fmt.Fprintf(w, "  Emulated:    %d\n", len(emulated))
	fmt.Fprintf(w, "  Unsupported: %d\n", len(unsupported))

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

	fidelity := 100.0
	if totalUnits > 0 {
		fidelity = float64(supportedUnits+len(emulated)) / float64(totalUnits) * 100
	}
	fmt.Fprintf(w, "\nEstimated fidelity: %.0f%% (%d/%d units fully supported or emulated; %d unsupported)\n",
		fidelity, supportedUnits+len(emulated), totalUnits, len(unsupported))

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
