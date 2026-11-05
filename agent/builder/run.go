package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// RailpackVersion is the Railpack library's version (set with -ldflags from
// go.mod); it names the plan's writer and must match the frontend image.
var RailpackVersion = "dev"

// runScanSummary is `odyssey-agent builder scan-summary`, the last container of
// a scan Job: it counts Trivy's report and writes the ScanSummary to the
// termination message. A report it cannot read fails the Job, with the
// reason in the same place.
func runScanSummary() error {
	s, err := scanSummary()
	if err != nil {
		_ = writeResult(TerminationLog, Result{Reason: ReasonFailed, Message: err.Error()})
		return err
	}
	b, _ := json.Marshal(s)
	return os.WriteFile(TerminationLog, b, 0o644)
}

// Run is `odyssey-agent builder prepare|build|scan-summary`: it does the
// step, writes its Result to the termination message and exits non-zero on
// failure.
func Run(args []string) error {
	if len(args) != 1 || (args[0] != "prepare" && args[0] != "build" && args[0] != "scan-summary") {
		return errors.New("usage: odyssey-agent builder prepare|build|scan-summary")
	}
	if args[0] == "scan-summary" {
		return runScanSummary()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var r Result
	switch args[0] {
	case "prepare":
		o, err := prepareFromEnv()
		if err != nil {
			r = Result{Reason: ReasonFailed, Message: err.Error()}
		} else {
			r = Prepare(ctx, o)
		}
	case "build":
		r = Build(ctx, buildFromEnv())
	}
	if err := writeResult(TerminationLog, r); err != nil {
		fmt.Fprintln(os.Stderr, "write the result:", err)
	}
	if r.Reason != "" {
		return fmt.Errorf("%s: %s", r.Reason, r.Message)
	}
	return nil
}
