// Package accountmodule assembles the account domain: the service over
// IAMKit's connections and its HTTP handler. No schema, no events, no
// subscription. Only the composition root imports it.
package accountmodule

import (
	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/account/accountsvc"
	"github.com/Abraxas-365/odyssey/internal/account/adapters/accounthttp"
	"github.com/Abraxas-365/odyssey/internal/assistant"
)

type Deps struct {
	// Connections reach IAMKit (accountiam); nil when MCP is off.
	Connections account.Connections
	// Assistants name the template assistants' clients.
	Assistants assistant.Registered
	// Tools are the permissions of each MCP tool, by name.
	Tools map[string][]string
}

type Module struct {
	Queries  account.Queries
	Commands account.Commands
	HTTP     *accounthttp.Handler
}

func New(deps Deps) Module {
	svc := accountsvc.New(deps.Connections, deps.Assistants, deps.Tools)
	return Module{Queries: svc, Commands: svc, HTTP: accounthttp.New(svc, svc)}
}
