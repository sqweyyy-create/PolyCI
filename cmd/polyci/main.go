// Command polyci runs CI/CD pipelines locally in Docker containers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/polyci/polyci/internal/executor"
	"github.com/polyci/polyci/internal/gitlab"
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
	fmt.Fprintln(os.Stderr, "Usage: polyci run [-f .gitlab-ci.yml]")
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	file := fs.String("f", ".gitlab-ci.yml", "path to the CI config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *file, err)
	}

	p, err := gitlab.Parse(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", *file, err)
	}

	log := newTermLogger(os.Stdout)

	docker, err := executor.New(log)
	if err != nil {
		return err
	}
	defer docker.Close()

	if err := docker.Run(context.Background(), p); err != nil {
		return err
	}

	return nil
}
