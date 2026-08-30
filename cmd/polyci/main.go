// Command polyci runs CI/CD pipelines locally in Docker containers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/sqweyyy-create/PolyCI/internal/circleci"
	"github.com/sqweyyy-create/PolyCI/internal/debugger"
	"github.com/sqweyyy-create/PolyCI/internal/executor"
	"github.com/sqweyyy-create/PolyCI/internal/githubactions"
	"github.com/sqweyyy-create/PolyCI/internal/gitlab"
	"github.com/sqweyyy-create/PolyCI/internal/pipeline"
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
	case "plan":
		err = planCmd(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "Usage: polyci run [-provider gitlab|circleci|github-actions] [-f config file] [-debug] [-shell-on-fail]")
	fmt.Fprintln(os.Stderr, "       polyci plan [-provider gitlab|circleci|github-actions] [-f config file]")
}

// providerFlags registers the -provider/-f flags shared by run and plan.
func providerFlags(fs *flag.FlagSet) (provider, file *string) {
	provider = fs.String("provider", "gitlab", "CI provider config format to parse: gitlab, circleci, or github-actions")
	file = fs.String("f", "", "path to the CI config file (default: .gitlab-ci.yml for gitlab, .circleci/config.yml for circleci; required for github-actions)")
	return provider, file
}

// resolvePipeline picks the right parser for provider, applies its default
// config path when file is empty, reads and parses that file, and returns
// the resulting pipeline along with the file path actually used.
func resolvePipeline(provider, file string) (*pipeline.Pipeline, string, error) {
	var parse func([]byte) (*pipeline.Pipeline, error)
	switch provider {
	case "gitlab":
		parse = gitlab.Parse
		if file == "" {
			file = ".gitlab-ci.yml"
		}
	case "circleci":
		parse = circleci.Parse
		if file == "" {
			file = ".circleci/config.yml"
		}
	case "github-actions":
		parse = githubactions.Parse
		if file == "" {
			return nil, "", fmt.Errorf("-f is required for -provider github-actions (workflow files can be named anything under .github/workflows/)")
		}
	default:
		return nil, "", fmt.Errorf("unknown provider %q (want gitlab, circleci, or github-actions)", provider)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, file, fmt.Errorf("reading %s: %w", file, err)
	}

	p, err := parse(data)
	if err != nil {
		return nil, file, fmt.Errorf("parsing %s: %w", file, err)
	}
	return p, file, nil
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	provider, file := providerFlags(fs)
	debug := fs.Bool("debug", false, "pause after each step and ask whether to continue or abort")
	shellOnFail := fs.Bool("shell-on-fail", false, "on a failing step, drop into an interactive shell in its container")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, _, err := resolvePipeline(*provider, *file)
	if err != nil {
		return err
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

// planCmd parses the given config and prints its resolved pipeline
// structure — no Docker container is created, and no Docker engine
// connection is even attempted.
func planCmd(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	provider, file := providerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, resolvedFile, err := resolvePipeline(*provider, *file)
	if err != nil {
		return err
	}

	return printPlan(os.Stdout, resolvedFile, p)
}
