// Package policymodule assembles the policy domain: repository, CEL engine,
// service and HTTP handler, and the adapters that give each governed domain
// its own Policy port. Only the composition root imports it.
package policymodule

import (
	"io/fs"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/policy"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyai"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policycel"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyhttp"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policypg"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyproject"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyrelease"
	"github.com/Abraxas-365/odyssey/internal/policy/adapters/policyresource"
	"github.com/Abraxas-365/odyssey/internal/policy/policysvc"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB     *sqlx.DB
	Events event.Publisher
	// CEL tunes the expression engine; the zero value takes the defaults.
	CEL policycel.Options
}

type Module struct {
	Commands  policy.Commands
	Queries   policy.Queries
	Evaluator policy.Evaluator
	HTTP      *policyhttp.Handler

	// What each governed domain asks (one port per consumer).
	Release  release.Policy
	Resource resource.Policy
	AI       ai.Policy
	Project  project.Policy
}

func New(deps Deps) Module {
	svc := policysvc.New(policypg.New(deps.DB), policycel.New(deps.CEL), txn.Postgres{DB: deps.DB}, deps.Events)
	return Module{
		Commands: svc, Queries: svc, Evaluator: svc,
		HTTP:     policyhttp.New(svc, svc, svc),
		Release:  policyrelease.New(svc),
		Resource: policyresource.New(svc),
		AI:       policyai.New(svc),
		Project:  policyproject.New(svc),
	}
}

// Schema is the module's Postgres schema and migration owner.
const Schema = policypg.Schema

// Migrations are the module's *.up.sql files.
func Migrations() fs.FS { return policypg.Migrations() }
