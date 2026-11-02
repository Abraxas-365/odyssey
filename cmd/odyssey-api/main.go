// Command odyssey-api runs the Odyssey control plane API.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var err error
	switch {
	case len(os.Args) == 1 || os.Args[1] == "serve":
		err = bootstrap.Run()
	case os.Args[1] == "github-app" && len(os.Args) > 2 && os.Args[2] == "create":
		err = bootstrap.CreateGitHubApp(os.Args[3:])
	default:
		err = fmt.Errorf("unknown command %q (serve, github-app create)", strings.Join(os.Args[1:], " "))
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
