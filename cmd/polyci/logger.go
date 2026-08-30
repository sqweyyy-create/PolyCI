package main

import (
	"fmt"
	"io"
	"sync"
)

// termLogger prints pipeline progress to a terminal, prefixing every line
// with the job (and step, once running) so it's always clear what's
// currently executing. Independent jobs run concurrently, so every method
// locks mu to keep concurrent writers from interleaving mid-line.
type termLogger struct {
	mu sync.Mutex
	w  io.Writer
}

func newTermLogger(w io.Writer) *termLogger {
	return &termLogger{w: w}
}

func (l *termLogger) JobStart(jobName, stage, image string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "==> [%s] stage=%s image=%s\n", jobName, stage, image)
}

func (l *termLogger) StepStart(jobName, stepName, command string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "[%s] $ %s\n", jobName, command)
}

func (l *termLogger) StepOutput(jobName, stepName string, chunk []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "[%s] %s", jobName, chunk)
}

func (l *termLogger) StepDone(jobName, stepName string, exitCode int64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		fmt.Fprintf(l.w, "[%s] step %s errored: %v\n", jobName, stepName, err)
		return
	}
	if exitCode != 0 {
		fmt.Fprintf(l.w, "[%s] step %s exited %d\n", jobName, stepName, exitCode)
		return
	}
	fmt.Fprintf(l.w, "[%s] step %s ok (exit 0)\n", jobName, stepName)
}

func (l *termLogger) JobDone(jobName string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		fmt.Fprintf(l.w, "==> [%s] FAILED: %v\n", jobName, err)
		return
	}
	fmt.Fprintf(l.w, "==> [%s] done\n", jobName)
}

func (l *termLogger) JobSkipped(jobName string, reason error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "==> [%s] SKIPPED: %v\n", jobName, reason)
}
