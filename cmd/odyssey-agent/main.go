// Command odyssey-agent is Odyssey's data plane: it runs in each cluster and
// reconciles the desired state the control plane sends.
package main

import (
	"fmt"
	"os"

	"github.com/Abraxas-365/odyssey/agent"
)

func main() {
	if err := agent.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "odyssey-agent:", err)
		os.Exit(1)
	}
}
