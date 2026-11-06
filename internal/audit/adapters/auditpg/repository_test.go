package auditpg_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/audit/adapters/auditpg"
	"github.com/Abraxas-365/odyssey/internal/audit/auditsvc"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type payload struct {
	N int `json:"n"`
}

var user = identity.MustParseUserID("00000000-0000-4000-8000-000000000002")

func ev(org identity.OrganizationID, typ string, n int) event.Event {
	e := event.New(typ, 1, org, user, "widget/1", "Cambió el widget", payload{N: n})
	event.Stamp(context.Background(), &e)
	return e
}

func setup(t *testing.T) (*sqlx.DB, *auditsvc.Service) {
	return setupWith(t, auditsvc.Options{})
}

func setupWith(t *testing.T, opts auditsvc.Options) (*sqlx.DB, *auditsvc.Service) {
	db := pgtest.DB(t, migrate.Set{Owner: auditpg.Schema, Files: auditpg.Migrations()})
	return db, auditsvc.New(auditpg.New(db), txn.Postgres{DB: db}, opts)
}

// from is an event as a request through client made it, about a project.
func from(org identity.OrganizationID, actor identity.UserID, typ, client string, project uuid.UUID, at time.Time) event.Event {
	e := event.New(typ, 1, org, actor, "project/"+project.String(), "Algo pasó", map[string]string{"project_id": project.String()})
	e.OccurredAt = at
	event.Stamp(event.WithMeta(context.Background(), event.Meta{CorrelationID: "req", Client: client}), &e)
	return e
}

type names map[identity.UserID]string

func (n names) Name(_ context.Context, _ identity.OrganizationID, u identity.UserID) (string, error) {
	return n[u], nil
}

type down struct{ calls int }

func (d *down) Name(context.Context, identity.OrganizationID, identity.UserID) (string, error) {
	d.calls++
	return "", errors.New("iamkit is down")
}

const claude = "mcp:https://claude.ai/oauth/claude-code-client-metadata"

// TestChannelsProjectsAndPeople: the filters a TI person reads the trail by
// (channel, app, person), the weekly summary and the people list, each
// within its organization; channel and name are derived on read and stay
// outside the hash.
func TestChannelsProjectsAndPeople(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	ana, beto := identity.MustParseUserID(uuid.NewString()), identity.MustParseUserID(uuid.NewString())
	_, svc := setupWith(t, auditsvc.Options{Directory: names{ana: "Ana Pérez"}, Now: func() time.Time { return now }})
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	app, app2 := uuid.New(), uuid.New()
	day := 24 * time.Hour
	for _, e := range []event.Event{
		from(org, ana, "deployment.started", "web-console", app, now.Add(-day)),
		from(org, ana, "deployment.started", claude, app, now.Add(-2*day)),
		from(org, ana, "deployment.started", claude, app2, now.Add(-3*day)),
		from(org, identity.UserID{}, "deployment.started", "github", app2, now.Add(-4*day)),
		from(org, identity.UserID{}, "deployment.ready", "platform", app, now.Add(-day)),
		from(org, beto, "project.updated", "", app, now.Add(-20*day)), // before channels were recorded; outside the week
		from(other, beto, "deployment.started", "web-console", app, now.Add(-day)),
	} {
		if err := svc.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	count := func(f audit.Filter) int {
		t.Helper()
		page, err := svc.List(ctx, org, f, query.Pagination{})
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		return page.Page.Total
	}
	for ch, want := range map[string]int{"console": 1, "github": 1, "platform": 1, "assistant": 2,
		"assistant:claude_code": 2, "assistant:codex": 0, "assistant:other": 0, "unrecorded": 1} {
		if got := count(audit.Filter{Channel: ch}); got != want {
			t.Errorf("channel %s: %d entries, want %d", ch, got, want)
		}
	}
	if _, err := svc.List(ctx, org, audit.Filter{Channel: "fax"}, query.Pagination{}); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("an unknown channel is a validation error: %v", err)
	}
	if got := count(audit.Filter{Project: app}); got != 4 {
		t.Errorf("project filter: %d", got)
	}
	if got := count(audit.Filter{Project: app2, Channel: "assistant"}); got != 1 {
		t.Errorf("project and channel: %d", got)
	}

	page, _ := svc.List(ctx, org, audit.Filter{Project: app}, query.Pagination{})
	byType := map[string]audit.Entry{}
	for _, e := range page.Items {
		byType[e.Type+"/"+e.Client] = e
		if h, err := e.ComputeHash(); err != nil || h != e.Hash {
			t.Errorf("derived fields leaked into the hash: %+v", e)
		}
	}
	if e := byType["deployment.started/"+claude]; e.Channel != "assistant:claude_code" || e.ActorName != "Ana Pérez" {
		t.Errorf("claude entry: %q %q", e.Channel, e.ActorName)
	}
	if e := byType["deployment.ready/platform"]; e.Channel != "platform" || e.ActorName != "" {
		t.Errorf("platform entry: %q %q", e.Channel, e.ActorName)
	}
	if e := byType["project.updated/"]; e.Channel != "" || e.ActorName != "" {
		t.Errorf("unrecorded entry of someone the directory does not know: %q %q", e.Channel, e.ActorName)
	}

	sum, err := svc.Summary(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Events != 5 || sum.Deployments != 4 || sum.People != 1 || !sum.From.Equal(now.Add(-7*day)) {
		t.Errorf("summary %+v", sum)
	}
	want := []audit.ChannelCount{{Channel: "assistant:claude_code", Count: 2}, {Channel: "console", Count: 1}, {Channel: "github", Count: 1}}
	if !reflect.DeepEqual(sum.ByChannel, want) {
		t.Errorf("by channel %+v", sum.ByChannel)
	}
	if empty, err := svc.Summary(ctx, identity.NewOrganizationID()); err != nil || empty.Events != 0 || empty.ByChannel == nil {
		t.Errorf("an empty trail sums to zero, with a list: %+v %v", empty, err)
	}

	people, err := svc.People(ctx, org)
	if err != nil || len(people) != 2 || people[0].Actor != ana || people[0].Name != "Ana Pérez" || people[0].Entries != 3 ||
		people[1].Actor != beto || people[1].Name != "" {
		t.Fatalf("people %+v %v", people, err)
	}
	if theirs, _ := svc.People(ctx, other); len(theirs) != 1 || theirs[0].Actor != beto {
		t.Errorf("another organization's people: %+v", theirs)
	}
}

// TestADirectoryThatFailsLeavesNamesEmpty: the trail stands without names,
// and one listing asks a failing directory once.
func TestADirectoryThatFailsLeavesNamesEmpty(t *testing.T) {
	dir := &down{}
	_, svc := setupWith(t, auditsvc.Options{Directory: dir})
	ctx := context.Background()
	org := identity.NewOrganizationID()
	for range 3 {
		if err := svc.Record(ctx, from(org, identity.MustParseUserID(uuid.NewString()), "project.updated", "web", uuid.New(), time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, org, audit.Filter{}, query.Pagination{})
	if err != nil || page.Page.Total != 3 || page.Items[0].ActorName != "" || dir.calls != 1 {
		t.Fatalf("list %+v %v (directory asked %d times)", page.Page, err, dir.calls)
	}
	if people, err := svc.People(ctx, org); err != nil || len(people) != 3 {
		t.Fatalf("people %+v %v", people, err)
	}
}

func TestChainIsIdempotentOrderedAndPerOrganization(t *testing.T) {
	_, svc := setup(t)
	ctx := context.Background()
	org, other := identity.NewOrganizationID(), identity.NewOrganizationID()
	first := ev(org, "widget.created", 1)
	for _, e := range []event.Event{first, first, ev(org, "widget.changed", 2), ev(other, "widget.created", 1)} {
		if err := svc.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, org, audit.Filter{}, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page.Total != 2 || page.Items[0].Seq != 2 || page.Items[1].Seq != 1 || page.Items[0].PrevHash != page.Items[1].Hash {
		t.Fatalf("chain %+v", page)
	}
	if v, _ := svc.Verify(ctx, other); !v.Valid || v.Entries != 1 {
		t.Fatalf("other organization has its own chain: %+v", v)
	}
	page, _ = svc.List(ctx, org, audit.Filter{Types: []string{"widget.changed"}, Actor: user}, query.Pagination{})
	if page.Page.Total != 1 {
		t.Fatalf("filter %+v", page.Page)
	}
	page, _ = svc.List(ctx, org, audit.Filter{CorrelationID: first.CorrelationID}, query.Pagination{Search: "widget"})
	if page.Page.Total != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("correlation filter %+v", page.Page)
	}
}

func TestConcurrentRecordsKeepTheChainLinear(t *testing.T) {
	_, svc := setup(t)
	ctx := context.Background()
	org := identity.NewOrganizationID()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := svc.Record(ctx, ev(org, "widget.changed", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if v, err := svc.Verify(ctx, org); err != nil || !v.Valid || v.Entries != 20 {
		t.Fatalf("verify %+v %v", v, err)
	}
}

func TestTamperingIsRefusedOrDetected(t *testing.T) {
	db, svc := setup(t)
	ctx := context.Background()
	org := identity.NewOrganizationID()
	for i := range 3 {
		if err := svc.Record(ctx, ev(org, "widget.changed", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE audit.entries SET summary = 'nada' WHERE seq = 2`); err == nil {
		t.Fatal("updates must be rejected")
	}
	if _, err := db.Exec(`DELETE FROM audit.entries WHERE seq = 2`); err == nil {
		t.Fatal("deletes must be rejected")
	}
	// someone with DDL rights disables the guard and edits history
	if _, err := db.Exec(`ALTER TABLE audit.entries DISABLE TRIGGER entries_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE audit.entries SET summary = 'nada' WHERE organization_id = $1 AND seq = 2`, org); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx, org); err != nil || v.Valid || v.BrokenAt == nil || *v.BrokenAt != 2 {
		t.Fatalf("verify after tampering %+v %v", v, err)
	}

	// cutting off the newest entries leaves a valid-looking prefix: the head catches it
	other := identity.NewOrganizationID()
	for i := range 3 {
		if err := svc.Record(ctx, ev(other, "widget.changed", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM audit.entries WHERE organization_id = $1 AND seq = 3`, other); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx, other); err != nil || v.Valid || v.BrokenAt == nil || *v.BrokenAt != 3 {
		t.Fatalf("verify after cutting the tail %+v %v", v, err)
	}
	if _, err := db.Exec(`ALTER TABLE audit.entries ENABLE TRIGGER entries_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE audit.entries`); err == nil {
		t.Fatal("truncate must be rejected")
	}
}
