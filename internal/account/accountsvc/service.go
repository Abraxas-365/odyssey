// Package accountsvc implements account.Queries and account.Commands over
// IAMKit (the Connections port): it keeps nothing.
package accountsvc

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

type Service struct {
	conns account.Connections
	// registered names the template assistants' clients (Cursor,
	// Antigravity); tools are the MCP tools' permissions, by name.
	registered assistant.Registered
	tools      map[string][]string
}

// New returns the service. conns nil (MCP off, or no IAMKit) leaves /me
// without an assistant.
func New(conns account.Connections, registered assistant.Registered, tools map[string][]string) *Service {
	return &Service{conns: conns, registered: registered, tools: tools}
}

var (
	_ account.Queries  = (*Service)(nil)
	_ account.Commands = (*Service)(nil)
)

func (s *Service) Me(ctx context.Context, organization identity.OrganizationID, caller account.Caller) (account.Me, error) {
	perms := slices.Clone(caller.Permissions)
	if perms == nil {
		perms = []string{}
	}
	slices.Sort(perms)
	me := account.Me{User: caller.User, Organization: organization, Role: account.Role(perms), Permissions: perms}
	if s.conns == nil {
		return me, nil
	}
	connected, err := s.conns.List(ctx, organization, caller.Token)
	if err != nil {
		// "No conectado" would be a guess: say nothing instead.
		slog.WarnContext(ctx, "cannot read the assistants connected to the user from IAMKit", "error", err)
		return me, nil
	}
	a := &account.Assistant{Connected: make([]account.Connection, 0, len(connected)), Abilities: assistant.AbilitiesFor(s.tools, perms)}
	for _, c := range connected {
		key := assistant.PresetOf(c.ClientID, s.registered)
		a.Connected = append(a.Connected, account.Connection{ID: c.ID, Key: key, Name: assistant.Names[key], ConsentedAt: c.ConsentedAt, LastUsedAt: c.LastUsedAt})
	}
	// The one used last first (that is the one the header names); then the
	// one connected last, then by id: the same answer every time.
	slices.SortFunc(a.Connected, func(x, y account.Connection) int {
		if c := compareTimes(y.LastUsedAt, x.LastUsedAt); c != 0 {
			return c
		}
		if c := compareTimes(y.ConsentedAt, x.ConsentedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	me.Assistant = a
	return me, nil
}

// compareTimes orders nil before any time.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

func (s *Service) Disconnect(ctx context.Context, organization identity.OrganizationID, caller account.Caller, connection string) error {
	if connection == "" {
		return errx.InvalidField("id", "id is required")
	}
	if s.conns == nil {
		return errx.NotFound("assistant connection not found")
	}
	// Only what is connected to Odicea's MCP server: the user's other apps
	// on IAMKit are none of Odicea's business.
	connected, err := s.conns.List(ctx, organization, caller.Token)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(connected, func(c account.Connected) bool { return c.ID == connection }) {
		return errx.NotFound("assistant connection not found")
	}
	return s.conns.Disconnect(ctx, organization, caller.Token, connection)
}
