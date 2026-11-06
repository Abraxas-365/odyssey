package bootstrap_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Abraxas-365/odyssey/internal/bootstrap"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
)

// TestUploadTickets covers the ticket's life in the service over Postgres:
// issued with the client of the request, redeemed once into a deployment of
// that client, then used / expired / unknown, and never for an archived app.
func TestUploadTickets(t *testing.T) {
	db := pgtest.DB(t)
	ctx := context.Background()
	if err := bootstrap.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	platform, err := bootstrap.Assemble(ctx, db, bootstrap.Options{Runner: buildfake.New(buildfake.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platform.Close() })
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	newApp := func(name string) (project.ID, project.EnvironmentID) {
		t.Helper()
		id, err := platform.Projects.Commands.Create(ctx, org, project.Create{Name: name, CreatedBy: user})
		if err != nil {
			t.Fatal(err)
		}
		envs, err := platform.Projects.Queries.ListEnvironments(ctx, org, id, project.EnvironmentFilter{})
		if err != nil || len(envs) == 0 {
			t.Fatalf("environments %v %v", envs, err)
		}
		return id, envs[0].ID
	}
	as := func(client string) context.Context {
		return event.WithMeta(ctx, event.Meta{CorrelationID: "req-" + client, Client: client})
	}
	cfg := release.Config{Port: 3000, HealthPath: "/"}
	issue := func(pid project.ID, env project.EnvironmentID) release.UploadTicket {
		t.Helper()
		tk, err := platform.Releases.Commands.IssueUpload(as("mcp:claude"), org, pid, env, release.IssueUpload{Name: "visitas", Message: " Agrega el\nfiltro ", Config: cfg, RequestedBy: user})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	code := func(err error) string {
		var x *errx.Error
		if errors.As(err, &x) {
			return x.Code
		}
		return ""
	}
	clientOf := func(typ, subject string) []string {
		t.Helper()
		var clients []string
		if err := db.SelectContext(ctx, &clients, `SELECT client FROM event.outbox WHERE organization_id = $1 AND type = $2 AND subject LIKE $3 ORDER BY seq`,
			org, typ, subject); err != nil {
			t.Fatal(err)
		}
		return clients
	}

	pid, env := newApp("Visitas")
	tk := issue(pid, env)
	if !release.ValidUploadToken(tk.Token) || tk.Client != "mcp:claude" || tk.UsedAt != nil ||
		time.Until(tk.ExpiresAt) > release.UploadTicketTTL || time.Until(tk.ExpiresAt) < release.UploadTicketTTL-time.Minute {
		t.Fatalf("issued %+v", tk)
	}
	var hashes int
	if err := db.GetContext(ctx, &hashes, `SELECT count(*) FROM release.upload_tickets WHERE token_hash = $1`, release.HashUploadToken(tk.Token)); err != nil || hashes != 1 {
		t.Fatalf("stored by hash: %d %v", hashes, err)
	}
	if got := clientOf(release.EventUploadTicketIssued, "upload_ticket/%"); len(got) != 1 || got[0] != "mcp:claude" {
		t.Fatalf("issued event clients %v", got)
	}

	// a bad configuration is refused at issue
	if _, err := platform.Releases.Commands.IssueUpload(as("mcp:claude"), org, pid, env,
		release.IssueUpload{Config: release.Config{Port: 70000}, RequestedBy: user}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("invalid config: %v", err)
	}
	// another organization's app is not found
	if _, err := platform.Releases.Commands.IssueUpload(as("x"), identity.NewOrganizationID(), pid, env,
		release.IssueUpload{Config: cfg}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other organization: %v", err)
	}

	// redeem: a deployment of the ticket's user, config, message and client, whatever the redeeming request says
	d, err := platform.Releases.Commands.RedeemUpload(as("none"), tk.Token, bytes.NewReader(folder(t, "one")))
	if err != nil {
		t.Fatal(err)
	}
	if d.Organization != org || d.Project != pid || d.Environment != env || d.RequestedBy != user || d.Source == nil ||
		d.Config == nil || d.Config.Port != 3000 || d.Source.Name != "visitas" || d.Message != "Agrega el filtro" {
		t.Fatalf("deployment %+v", d)
	}
	if d.RequestedVia != "assistant:other" { // "mcp:claude" is no known preset
		t.Fatalf("requested_via %q", d.RequestedVia)
	}
	if got := clientOf(release.EventDeploymentStarted, "deployment/"+d.ID.String()); len(got) != 1 || got[0] != "mcp:claude" {
		t.Fatalf("deployment.started clients %v (want the ticket's)", got)
	}
	if got := clientOf(release.EventUploadTicketUsed, "upload_ticket/%"); len(got) != 1 || got[0] != "mcp:claude" {
		t.Fatalf("used event clients %v", got)
	}
	var payloads []string
	if err := db.SelectContext(ctx, &payloads, `SELECT payload::text FROM event.outbox WHERE organization_id = $1 AND type LIKE 'upload_ticket.%'`, org); err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if strings.Contains(p, tk.Token) || strings.Contains(p, "token") {
			t.Fatalf("a ticket event carries the token: %s", p)
		}
	}
	var recorded *uuid.UUID
	if err := db.GetContext(ctx, &recorded, `SELECT deployment_id FROM release.upload_tickets WHERE id = $1`, tk.ID); err != nil || recorded == nil || *recorded != d.ID.UUID() {
		t.Fatalf("recorded deployment %v %v", recorded, err)
	}

	// reuse: used, naming the deployment it started
	_, err = platform.Releases.Commands.RedeemUpload(ctx, tk.Token, bytes.NewReader(folder(t, "two")))
	var x *errx.Error
	if !errors.As(err, &x) || !errx.IsType(err, errx.TypeConflict) || x.Code != release.CodeUploadTicketUsed || x.Details["deployment_id"] != d.ID.String() {
		t.Fatalf("reused ticket: %v", err)
	}

	// expired: business error, and nothing started
	old := issue(pid, env)
	if _, err := db.ExecContext(ctx, `UPDATE release.upload_tickets SET created_at = now() - interval '20 minutes', expires_at = now() - interval '10 minutes' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Releases.Commands.RedeemUpload(ctx, old.Token, bytes.NewReader(folder(t, "late"))); !errx.IsType(err, errx.TypeBusiness) || code(err) != release.CodeUploadTicketExpired {
		t.Fatalf("expired ticket: %v", err)
	}
	var usedAt *time.Time
	if err := db.GetContext(ctx, &usedAt, `SELECT used_at FROM release.upload_tickets WHERE id = $1`, old.ID); err != nil || usedAt != nil {
		t.Fatalf("an expired ticket was spent: %v %v", usedAt, err)
	}

	// unknown and malformed: not found
	for _, token := range []string{"", "nope", release.UploadTicketPrefix, "odu_" + strings.Repeat("A", 43), strings.ToUpper(tk.Token)} {
		if _, err := platform.Releases.Commands.RedeemUpload(ctx, token, bytes.NewReader(folder(t, "x"))); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("token %q: %v", token, err)
		}
	}

	// a refused archive still spends the ticket (the retry is a new ticket)
	broken := issue(pid, env)
	if _, err := platform.Releases.Commands.RedeemUpload(ctx, broken.Token, strings.NewReader("this is not a tar.gz")); err == nil {
		t.Fatal("garbage was published")
	}
	if _, err := platform.Releases.Commands.RedeemUpload(ctx, broken.Token, bytes.NewReader(folder(t, "again"))); code(err) != release.CodeUploadTicketUsed {
		t.Fatalf("ticket after a refused archive: %v", err)
	}

	// tickets expired for more than a day are swept by the organization's next one
	if _, err := db.ExecContext(ctx, `UPDATE release.upload_tickets SET created_at = now() - interval '3 days', expires_at = now() - interval '2 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	issue(pid, env)
	var left int
	if err := db.GetContext(ctx, &left, `SELECT count(*) FROM release.upload_tickets WHERE id = $1`, old.ID); err != nil || left != 0 {
		t.Fatalf("the sweep kept it: %d %v", left, err)
	}

	// an archived app gets no ticket
	archived, archivedEnv := newApp("Archivada")
	if err := platform.Projects.Commands.Archive(ctx, org, archived, project.Archive{ArchivedBy: user}); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Releases.Commands.IssueUpload(as("mcp:claude"), org, archived, archivedEnv,
		release.IssueUpload{Config: cfg, RequestedBy: user}); code(err) != release.CodeArchived {
		t.Fatalf("archived app: %v", err)
	}
}
