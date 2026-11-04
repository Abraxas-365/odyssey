// Command odyssey-agent is Odyssey's data plane: it runs in each cluster and
// reconciles the desired state the control plane sends.
package main

import (
	"fmt"
	"os"

	"github.com/Abraxas-365/odyssey/agent"
	"github.com/Abraxas-365/odyssey/agent/access"
	"github.com/Abraxas-365/odyssey/agent/builder"
)

func main() {
	run := agent.Run
	if len(os.Args) > 1 && os.Args[1] == "builder" {
		run = builder.Run // a build's pod runs this binary too (agent/builder)
		os.Args = os.Args[1:]
	}
	if len(os.Args) > 1 && os.Args[1] == "access" {
		run = access.Run // the sign-in gate (agent/access): another Deployment of the same image
		os.Args = os.Args[1:]
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "odyssey-agent:", err)
		os.Exit(1)
	}
}
