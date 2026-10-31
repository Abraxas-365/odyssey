package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// RailpackVersion is the Railpack library's version (set with -ldflags from
// go.mod); it names the plan's writer and must match the frontend image.
var RailpackVersion = "dev"

// Run is `odyssey-agent builder prepare|build`: it does the step, writes
// its Result to the termination message and exits non-zero on failure.
func Run(args []string) error {
	if len(args) != 1 || (args[0] != "prepare" && args[0] != "build") {
		return errors.New("usage: odyssey-agent builder prepare|build")
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
