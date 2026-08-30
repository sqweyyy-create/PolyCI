// Package debugger implements the pause/resume layer on top of the Docker
// executor: after every step it shows the step's result and asks the user
// whether the pipeline should continue or abort.
package debugger

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/polyci/polyci/internal/executor"
	"github.com/polyci/polyci/internal/pipeline"
)

// Interactive is an executor.StepController that prompts a human on in/out
// after every step.
type Interactive struct {
	in  *bufio.Reader
	out io.Writer
}

// NewInteractive builds an Interactive controller reading prompts from in
// and writing them to out — typically os.Stdin and os.Stdout.
func NewInteractive(in io.Reader, out io.Writer) *Interactive {
	return &Interactive{in: bufio.NewReader(in), out: out}
}

// AfterStep implements executor.StepController. The result of the step was
// already logged by the executor's Logger just before this is called; here
// we only need to prompt for the next move.
func (i *Interactive) AfterStep(jobName string, step pipeline.Step, exitCode int64, stepErr error) executor.Decision {
	for {
		fmt.Fprintf(i.out, "[%s] step %q done — continue? [Y/n/a=abort] ", jobName, step.Name)

		line, err := i.in.ReadString('\n')
		if err != nil {
			fmt.Fprintln(i.out, "\nno input available, aborting")
			return executor.Abort
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return executor.Continue
		case "n", "a", "abort":
			return executor.Abort
		default:
			fmt.Fprintln(i.out, "please answer y (continue) or a (abort)")
		}
	}
}
