// Command polyci runs CI/CD pipelines locally in Docker containers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/polyci/polyci/internal/circleci"
	"github.com/polyci/polyci/internal/debugger"
	"github.com/polyci/polyci/internal/executor"
	"github.com/polyci/polyci/internal/gitlab"
	"github.com/polyci/polyci/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: polyci run [-provider gitlab|circleci] [-f config file] [-debug] [-shell-on-fail]")
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	provider := fs.String("provider", "gitlab", "CI provider config format to parse: gitlab or circleci")
	file := fs.String("f", "", "path to the CI config file (default: .gitlab-ci.yml for gitlab, .circleci/config.yml for circleci)")
	debug := fs.Bool("debug", false, "pause after each step and ask whether to continue or abort")
	shellOnFail := fs.Bool("shell-on-fail", false, "on a failing step, drop into an interactive shell in its container")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var parse func([]byte) (*pipeline.Pipeline, error)
	switch *provider {
	case "gitlab":
		parse = gitlab.Parse
		if *file == "" {
			*file = ".gitlab-ci.yml"
		}
	case "circleci":
		parse = circleci.Parse
		if *file == "" {
			*file = ".circleci/config.yml"
		}
	default:
		return fmt.Errorf("unknown provider %q (want gitlab or circleci)", *provider)
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *file, err)
	}

	p, err := parse(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", *file, err)
	}

	log := newTermLogger(os.Stdout)

	var opts []executor.Option
	if *debug || *shellOnFail {
		ctrl := debugger.NewInteractive(os.Stdin, os.Stdout)
		ctrl.StepByStep = *debug
		ctrl.ShellOnFail = *shellOnFail
		opts = append(opts, executor.WithController(ctrl))
	}

	docker, err := executor.New(log, opts...)
	if err != nil {
		return err
	}
	defer docker.Close()

	if err := docker.Run(context.Background(), p); err != nil {
		return err
	}

	return nil
}
