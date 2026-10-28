// Command odyssey-gateway is where cluster agents connect: they enrol with
// a one-time token, then keep an outbound mutual-TLS tunnel through which
// Odyssey sends what each cluster must run and receives what it sees. It
// holds the fleet CA key; odyssey-api and odyssey-worker never do.
//
//	odyssey-gateway [run]                       serve (FLEET_* environment)
//	odyssey-gateway ca init <cert.pem> <key.pem> create the fleet CA (once)
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
		err = bootstrap.RunGateway()
	case len(os.Args) == 5 && os.Args[1] == "ca" && os.Args[2] == "init":
		err = bootstrap.InitFleetCA(os.Args[3], os.Args[4])
	default:
		err = fmt.Errorf("usage: odyssey-gateway [run] | odyssey-gateway ca init <cert.pem> <key.pem>")
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
