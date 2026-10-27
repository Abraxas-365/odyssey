// Command odyssey-worker runs Odyssey's background work: it delivers domain
// events from the outbox to their subscribers (the audit trail, and later
// the reactions between modules) and executes durable workflows and
// scheduled tasks. Run one or more replicas next to odyssey-api.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var err error
	switch {
	case len(os.Args) == 1 || os.Args[1] == "run":
		err = bootstrap.RunWorker()
	case os.Args[1] == "deliveries":
		err = bootstrap.Deliveries(os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q (run | deliveries dead | deliveries retry <subscriber>)", os.Args[1])
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
