package sourcesvc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
)

const sha3 = "cccccccccccccccccccccccccccccccccccccccc"

// deliver sends a signed webhook like GitHub would.
func (e *env) deliver(delivery, event string, body []byte) error {
	return e.svc.Receive(context.Background(), source.Webhook{Delivery: delivery, Event: event,
		Signature: e.provider.Sign(body), Payload: body})
}

func (e *env) pushBody(branch, sha string) []byte {
	return sourcefake.PushPayload(installExt, repoWeb, "acme/web", branch, source.Commit{SHA: sha, Title: "t", Author: "ana"})
}

func TestReceivePush(t *testing.T) {
	t.Run("queues once; a repeated delivery is a no-op", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.events.Reset()
		body := e.pushBody("main", sha2)
		if err := e.deliver("d-1", "push", body); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventPushReceived)
		ps := e.pushes(t, id)
		if len(ps) != 2 || ps[0].Trigger != source.TriggerWebhook || ps[0].Git.Commit != sha2 || !ps[0].RequestedBy.IsZero() {
			t.Fatalf("pushes = %+v", ps)
		}
		if !e.engine.has(ps[0].ID.String()) {
			t.Fatal("no run started")
		}
		if err := e.deliver("d-1", "push", body); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventPushReceived)
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d after a repeated delivery", n)
		}
	})

	t.Run("the same commit through another delivery queues nothing", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		body := e.pushBody("main", sha2)
		if err := e.deliver("d-1", "push", body); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("d-2", "push", body); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d, want the connect's and one more", n)
		}
		// the head the connect already published is not queued either
		if err := e.deliver("d-3", "push", e.pushBody("main", sha1)); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("d-4", "push", e.pushBody("main", sha1)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 3 {
			t.Fatalf("pushes = %d", n) // sha1 differs from the last (sha2): queued once
		}
	})

	t.Run("ignored pushes", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.events.Reset()
		unknown := sourcefake.PushPayload(999, repoWeb, "acme/web", "main", source.Commit{SHA: sha2})
		zeros := strings.Repeat("0", 40)
		for i, body := range [][]byte{
			e.pushBody("dev", sha2), // another branch
			unknown,                 // an installation nobody linked
			e.pushBody("main", zeros),
			e.pushBody("main", ""),
			sourcefake.PushPayload(installExt, 4242, "acme/other", "main", source.Commit{SHA: sha2}), // no connection
		} {
			if err := e.deliver("ign-"+string(rune('a'+i)), "push", body); err != nil {
				t.Fatalf("%d: %v", i, err)
			}
		}
		if err := e.deliver("ign-tag", "push", []byte(`{"ref":"refs/tags/v1","after":"`+sha2+`","repository":{"id":42},"installation":{"id":7}}`)); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("ign-del", "push", []byte(`{"ref":"refs/heads/main","after":"`+sha2+`","deleted":true,"repository":{"id":42},"installation":{"id":7}}`)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 1 {
			t.Fatalf("pushes = %d", n)
		}
		e.wantEvents(t)
	})

	t.Run("a branch other than the connected one is ignored, the connected one is not", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		onDev, _ := e.connect(t, inst, source.Connect{Branch: "dev"})
		onMain, _ := e.connect(t, inst, source.Connect{})
		if err := e.deliver("d-1", "push", e.pushBody("dev", sha3)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, onDev)); n != 2 {
			t.Fatalf("dev pushes = %d", n)
		}
		if n := len(e.pushes(t, onMain)); n != 1 {
			t.Fatalf("main pushes = %d", n)
		}
	})

	t.Run("archived projects and suspended installations", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		e.projects.archive(id)
		if err := e.deliver("d-1", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 1 {
			t.Fatalf("pushes = %d for an archived project", n)
		}
		// the archived project did not "see" the commit: a delivery later queues it
		e.projects.list[id].Status = project.StatusActive
		if err := e.deliver("d-2", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d once active again", n)
		}

		if err := e.deliver("sus", "installation", sourcefake.InstallationPayload("suspend", installExt)); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("d-3", "push", e.pushBody("main", sha3)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d for a suspended installation", n)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := setup(t)
		e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		body := e.pushBody("main", sha2)
		err := e.svc.Receive(context.Background(), source.Webhook{Delivery: "x", Event: "push", Signature: "sha256=00", Payload: body})
		wantType(t, "bad signature", err, errx.TypeAuthorization)
		// a bad signature does not burn the delivery id
		if err := e.deliver("x", "push", body); err != nil {
			t.Fatal(err)
		}
		err = e.svc.Receive(context.Background(), source.Webhook{Event: "push", Signature: e.provider.Sign(body), Payload: body})
		wantType(t, "no delivery id", err, errx.TypeValidation)
		err = e.deliver("bad", "push", []byte(`not json`))
		wantType(t, "malformed", err, errx.TypeValidation)
		// the failed delivery is not remembered: the redelivery is handled afresh
		if err := e.deliver("bad", "push", body); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("p", "ping", []byte(`{}`)); err != nil {
			t.Fatalf("ping: %v", err)
		}
		if err := e.deliver("u", "star", []byte(`{}`)); err != nil {
			t.Fatalf("unknown event: %v", err)
		}

		off := newEnv(t, true)
		err = off.svc.Receive(context.Background(), source.Webhook{Delivery: "x", Event: "push", Payload: body})
		wantCode(t, "not configured", err, source.CodeNotConfigured)
	})

	t.Run("another organization's connection is not touched", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		// the repository is also reachable from a second installation row in
		// another organization (cross-org lookups must compare organizations)
		if err := e.deliver("d-1", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		for _, p := range e.pushes(t, id) {
			if p.Organization != e.org {
				t.Fatalf("push of org %s", p.Organization)
			}
		}
	})
}

func TestReceiveInstallation(t *testing.T) {
	t.Run("deleted unlinks like an uninstall", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		e.events.Reset()
		if err := e.deliver("d-1", "installation", sourcefake.InstallationPayload("deleted", installExt)); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventDisconnected, source.EventInstallationUnlinked)
		if _, err := e.svc.FindConnection(context.Background(), e.org, id); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("connection: %v", err)
		}
		if l, _ := e.svc.ListInstallations(context.Background(), e.org); len(l) != 0 {
			t.Fatalf("installations = %+v", l)
		}
		if d := e.eventsOf(source.EventDisconnected)[0]; !strings.Contains(string(d.Payload), source.ReasonUninstalled) || !d.Actor.IsZero() {
			t.Fatalf("disconnected = %s actor %s", d.Payload, d.Actor)
		}
		// a repeated "deleted" finds nothing and is ignored
		if err := e.deliver("d-2", "installation", sourcefake.InstallationPayload("deleted", installExt)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("suspend and unsuspend", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		e.events.Reset()
		// the provider says it is suspended
		e.provider.AddInstallation(source.InstallationInfo{ID: installExt, Account: "acme", AccountType: "Organization", Suspended: true})
		if err := e.deliver("s-1", "installation", sourcefake.InstallationPayload("suspend", installExt)); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventInstallationChanged)
		got := e.installation(t, inst.ID)
		if !got.Suspended || got.LinkedBy != e.user || got.Organization != e.org {
			t.Fatalf("installation = %+v", got)
		}

		e.provider.AddInstallation(source.InstallationInfo{ID: installExt, Account: "acme", AccountType: "Organization"})
		if err := e.deliver("s-2", "installation", sourcefake.InstallationPayload("unsuspend", installExt)); err != nil {
			t.Fatal(err)
		}
		if got := e.installation(t, inst.ID); got.Suspended {
			t.Fatal("still suspended")
		}
		e.wantEvents(t, source.EventInstallationChanged, source.EventInstallationChanged)
	})

	t.Run("a failing refresh applies what the action implies", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		e.provider.Fail("Installation", errx.External("github down"))
		if err := e.deliver("s-1", "installation", sourcefake.InstallationPayload("suspend", installExt)); err != nil {
			t.Fatal(err)
		}
		if !e.installation(t, inst.ID).Suspended {
			t.Fatal("not suspended")
		}
		if err := e.deliver("s-2", "installation", sourcefake.InstallationPayload("unsuspend", installExt)); err != nil {
			t.Fatal(err)
		}
		if e.installation(t, inst.ID).Suspended {
			t.Fatal("still suspended")
		}
	})

	t.Run("removed repositories disconnect their projects", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		e.events.Reset()
		if err := e.deliver("r-1", "installation_repositories", sourcefake.InstallationPayload("removed", installExt, 4242)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.FindConnection(context.Background(), e.org, id); err != nil {
			t.Fatalf("another repository's removal disconnected it: %v", err)
		}
		e.wantEvents(t, source.EventInstallationChanged)
		e.events.Reset()
		if err := e.deliver("r-2", "installation_repositories", sourcefake.InstallationPayload("removed", installExt, repoWeb)); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventInstallationChanged, source.EventDisconnected)
		if _, err := e.svc.FindConnection(context.Background(), e.org, id); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("connection: %v", err)
		}
		if d := e.eventsOf(source.EventDisconnected)[0]; !strings.Contains(string(d.Payload), source.ReasonRepositoryRemoved) {
			t.Fatalf("disconnected = %s", d.Payload)
		}
		e.installation(t, inst.ID) // the installation itself stays
	})

	t.Run("added publishes the change; created and unknown installations are ignored", func(t *testing.T) {
		e := setup(t)
		e.link(t, installExt, "code-1")
		e.events.Reset()
		if err := e.deliver("a-1", "installation_repositories", sourcefake.InstallationPayload("added", installExt)); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t, source.EventInstallationChanged)
		e.events.Reset()
		if err := e.deliver("a-2", "installation", sourcefake.InstallationPayload("created", installExt)); err != nil {
			t.Fatal(err)
		}
		if err := e.deliver("a-3", "installation", sourcefake.InstallationPayload("suspend", 999)); err != nil {
			t.Fatal(err)
		}
		e.wantEvents(t)
	})
}

func (e *env) installation(t *testing.T, id source.InstallationID) source.Installation {
	t.Helper()
	got, err := e.repo.FindInstallation(context.Background(), e.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("queues a commit missed by webhooks once, observes the rest", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.events.Reset()

		// the first poll sees the commit the connect already published
		n, err := e.svc.Reconcile(ctx)
		if err != nil || n != 0 {
			t.Fatalf("reconcile = %d, %v", n, err)
		}
		c, _ := e.svc.FindConnection(ctx, e.org, id)
		if c.ETag == "" {
			t.Fatal("the etag was not kept")
		}
		e.wantEvents(t)

		// not due yet
		e.provider.Push(repoWeb, "main", source.Commit{SHA: sha2, Title: "missed", Author: "ana"})
		if n, err := e.svc.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("too early: %d, %v", n, err)
		}

		e.offset = 3 * time.Minute
		if n, err := e.svc.Reconcile(ctx); err != nil || n != 1 {
			t.Fatalf("reconcile = %d, %v", n, err)
		}
		e.wantEvents(t, source.EventPushReceived)
		ps := e.pushes(t, id)
		if len(ps) != 2 || ps[0].Trigger != source.TriggerReconcile || ps[0].Git.Commit != sha2 {
			t.Fatalf("pushes = %+v", ps)
		}

		// unchanged: the etag goes back and nothing is queued
		e.offset = 6 * time.Minute
		if n, err := e.svc.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("unchanged = %d, %v", n, err)
		}
		// the webhook for that same commit arrives late: nothing new
		if err := e.deliver("late", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d", n)
		}
	})

	t.Run("a webhook that came first leaves nothing for the poll", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.provider.Push(repoWeb, "main", source.Commit{SHA: sha2, Title: "t", Author: "ana"})
		if err := e.deliver("d-1", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		if n, err := e.svc.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("reconcile = %d, %v", n, err)
		}
		if n := len(e.pushes(t, id)); n != 2 {
			t.Fatalf("pushes = %d", n)
		}
	})

	t.Run("skips a removed repository and archived projects", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		gone, _ := e.connect(t, inst, source.Connect{})
		e.provider.RemoveRepo(repoWeb)
		if n, err := e.svc.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("removed repository: %d, %v", n, err)
		}
		if n := len(e.pushes(t, gone)); n != 1 {
			t.Fatalf("pushes = %d", n)
		}

		e2 := setup(t)
		id, _ := e2.connect(t, e2.link(t, installExt, "code-1"), source.Connect{})
		e2.provider.Push(repoWeb, "main", source.Commit{SHA: sha2, Title: "t", Author: "ana"})
		e2.projects.archive(id)
		if n, err := e2.svc.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("archived: %d, %v", n, err)
		}
		if n := len(e2.pushes(t, id)); n != 1 {
			t.Fatalf("pushes = %d", n)
		}
	})

	t.Run("a provider outage stops the round with the error", func(t *testing.T) {
		e := setup(t)
		e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.provider.Fail("Head", errx.External("github down"))
		if _, err := e.svc.Reconcile(ctx); !errx.IsType(err, errx.TypeExternal) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("not configured does nothing", func(t *testing.T) {
		off := newEnv(t, true)
		if n, err := off.svc.Reconcile(ctx); n != 0 || err != nil {
			t.Fatalf("reconcile = %d, %v", n, err)
		}
	})
}

func TestReport(t *testing.T) {
	ctx := context.Background()

	t.Run("sets the status with a link to the app", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		long := strings.Repeat("é", 300)
		for _, st := range []source.CommitStatus{source.PreparingStatus(), source.PublishingStatus(), source.DeploymentFailedStatus(long)} {
			if err := e.svc.Report(ctx, e.org, source.Report{Push: push.ID, Status: st}); err != nil {
				t.Fatal(err)
			}
		}
		got := e.provider.Statuses(repoWeb, sha1)
		if len(got) != 3 || got[0].State != source.CommitPending || got[0].Description != "Preparando la publicación" ||
			got[2].State != source.CommitFailure || len([]rune(got[2].Description)) != 140 {
			t.Fatalf("statuses = %+v", got)
		}
		if want := "https://console.test/apps/" + id.String(); got[0].TargetURL != want {
			t.Fatalf("target = %q, want %q", got[0].TargetURL, want)
		}
	})

	t.Run("finds the push by its deployment", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, push, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		dep := e.deployer.all()[0].Result.ID
		if err := e.svc.Report(ctx, e.org, source.Report{Deployment: dep, Status: source.LiveStatus()}); err != nil {
			t.Fatal(err)
		}
		got := e.provider.Statuses(repoWeb, sha1)
		if len(got) != 1 || got[0].State != source.CommitSuccess || got[0].Description != "En línea" {
			t.Fatalf("statuses = %+v", got)
		}
	})

	t.Run("quietly ignores what it cannot report", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		push := e.onlyPush(t, id)
		report := func(r source.Report) {
			t.Helper()
			if err := e.svc.Report(ctx, e.org, r); err != nil {
				t.Fatalf("report: %v", err)
			}
		}
		// a deployment not published from git, an unknown push
		report(source.Report{Deployment: release.NewDeploymentID(), Status: source.LiveStatus()})
		report(source.Report{Push: source.NewPushID(), Status: source.LiveStatus()})
		// the provider failing is swallowed
		e.provider.Fail("SetStatus", errx.External("github down"))
		report(source.Report{Push: push.ID, Status: source.LiveStatus()})
		e.provider.Fail("SetStatus", nil)
		// another organization cannot reach the push
		if err := e.svc.Report(ctx, identity.NewOrganizationID(), source.Report{Push: push.ID, Status: source.LiveStatus()}); err != nil {
			t.Fatal(err)
		}
		// reconnected: the old push's connection is gone
		if err := e.svc.Disconnect(ctx, e.org, id, source.Disconnect{DisconnectedBy: e.user}); err != nil {
			t.Fatal(err)
		}
		report(source.Report{Push: push.ID, Status: source.LiveStatus()})
		e.connectProject(t, id, inst, source.Connect{})
		report(source.Report{Push: push.ID, Status: source.LiveStatus()})
		if got := e.provider.Statuses(repoWeb, sha1); len(got) != 0 {
			t.Fatalf("statuses = %+v", got)
		}
	})

	t.Run("a suspended installation gets none", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.deliver("s", "installation", sourcefake.InstallationPayload("suspend", installExt)); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Report(ctx, e.org, source.Report{Push: push.ID, Status: source.LiveStatus()}); err != nil {
			t.Fatal(err)
		}
		if got := e.provider.Statuses(repoWeb, sha1); len(got) != 0 {
			t.Fatalf("statuses = %+v", got)
		}
	})
}
