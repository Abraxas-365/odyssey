// Package auditmodule assembles the audit domain: repository, service, HTTP
// handler and its subscription to every event. Only the composition root
// imports it.
package auditmodule

import (
	"context"
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/audit/adapters/audithttp"
	"github.com/Abraxas-365/odyssey/internal/audit/adapters/auditpg"
	"github.com/Abraxas-365/odyssey/internal/audit/auditsvc"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

// Subscription is the trail's subscriber name in the outbox.
const Subscription = "audit"

type Deps struct {
	DB *sqlx.DB
	// Assistants name the assistants' channels (config MCP.TemplateClients).
	Assistants assistant.Registered
	// Directory names the people (IAMKit's members; nil: no names).
	Directory audit.Directory
}

type Module struct {
	Commands audit.Commands
	Queries  audit.Queries
	HTTP     *audithttp.Handler
}

func New(deps Deps) Module {
	svc := auditsvc.New(auditpg.New(deps.DB), txn.Postgres{DB: deps.DB},
		auditsvc.Options{Assistants: deps.Assistants, Directory: deps.Directory})
	return Module{Commands: svc, Queries: svc, HTTP: audithttp.New(svc)}
}

// Subscribe records every event, in order per organization (the hash chain
// has no gaps: a failing event holds back that organization's later ones).
func (m Module) Subscribe(bus *event.Bus) {
	bus.Add(event.Subscription{Name: Subscription, Ordered: true, Handle: func(ctx context.Context, e event.Event) error {
		return m.Commands.Record(ctx, e)
	}})
}

// Schema is the module's Postgres schema and migration owner.
const Schema = auditpg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return auditpg.Migrations() }
