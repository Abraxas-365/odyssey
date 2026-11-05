package sourcesvc

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/source"
)

const (
	// deliveryMemory is how long a delivery id is remembered: GitHub
	// redelivers within hours, and a manual redelivery stays in its UI for days.
	deliveryMemory = 7 * 24 * time.Hour
	// reconcileBatch bounds the connections one poll round checks.
	reconcileBatch = 200
)

// Webhook events (X-GitHub-Event) the service acts on.
const (
	eventPush                 = "push"
	eventInstallation         = "installation"
	eventInstallationRepos    = "installation_repositories"
	actionDeleted             = "deleted"
	actionPermissionsAccepted = "new_permissions_accepted"
	actionAdded               = "added"
	actionRemoved             = "removed"
)

// ---- webhooks ----

// Receive handles one delivery. Order: the provider is configured, the
// signature holds, the delivery is well formed, then one transaction records
// the delivery id (a repeated one stops there) and does everything the
// delivery causes. The transaction rolls back on an error, delivery id
// included, so the provider's redelivery is handled afresh.
func (s *Service) Receive(ctx context.Context, input source.Webhook) error {
	if err := s.enabled(); err != nil {
		return err
	}
	if err := s.provider.VerifyWebhook(input.Signature, input.Payload); err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		fresh, err := s.repo.ObserveDelivery(ctx, input.Delivery, s.opts.Now().Add(-deliveryMemory))
		if err != nil || !fresh {
			return err
		}
		switch input.Event {
		case eventPush:
			return s.receivePush(ctx, input.Payload)
		case eventInstallation, eventInstallationRepos:
			return s.receiveInstallation(ctx, input.Event, input.Payload)
		}
		return nil // ping and anything else: the delivery is recorded, nothing more
	})
}

// receivePush queues the pushed commit on every connection of that
// repository and branch that has not seen it yet.
func (s *Service) receivePush(ctx context.Context, payload []byte) error {
	ev, err := s.provider.PushEvent(payload)
	if err != nil {
		return err
	}
	if ev.Branch == "" || ev.Deleted || emptySHA(ev.Head.SHA) {
		return nil // a tag, a deleted branch: nothing to publish
	}
	inst, err := s.repo.FindInstallationByExternal(ctx, ev.Installation)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil // not linked to any organization here
	}
	if err != nil || inst.Suspended {
		return err
	}
	conns, err := s.repo.ListConnectionsByRepository(ctx, inst.ID, ev.RepositoryID)
	if err != nil {
		return err
	}
	// the lookups are cross-org: only the installation's organization counts
	conns = slices.DeleteFunc(conns, func(c source.Connection) bool {
		return c.Branch != ev.Branch || c.Organization != inst.Organization
	})
	slices.SortFunc(conns, func(a, b source.Connection) int { return strings.Compare(a.Project.String(), b.Project.String()) })
	for _, c := range conns {
		if err := s.queueMoved(ctx, c, ev.Head, source.TriggerWebhook, ev.Sender); err != nil {
			return err
		}
	}
	return nil
}

// queueMoved queues head on connection c under its lock when the connection
// is still the same one and has not seen that commit (a webhook and the poll,
// or two deliveries, report the same commit; it is published once), and its
// project is not archived. Publishes push_received.
func (s *Service) queueMoved(ctx context.Context, c source.Connection, head source.Commit, trigger source.Trigger, sender string) error {
	locked, err := s.repo.LockConnection(ctx, c.Organization, c.Project)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil // disconnected meanwhile
	}
	if err != nil {
		return err
	}
	if locked.ID != c.ID || locked.Branch != c.Branch || locked.RepositoryID != c.RepositoryID || locked.LastCommit == head.SHA {
		return nil
	}
	p, err := s.projects.Find(ctx, c.Organization, c.Project)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil || p.Archived() {
		return err
	}
	push, err := s.queue(ctx, locked, head, trigger, identity.UserID{}, github(sender))
	if err != nil {
		return err
	}
	return s.events.Publish(ctx, source.NewPushReceived(push))
}

// emptySHA: GitHub reports a deleted branch's head as forty zeros.
func emptySHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

// receiveInstallation applies a change GitHub made to an installation.
// "created" is ignored: linking goes only through setup + claim.
func (s *Service) receiveInstallation(ctx context.Context, name string, payload []byte) error {
	ev, err := s.provider.InstallationEvent(name, payload)
	if err != nil {
		return err
	}
	inst, err := s.repo.FindInstallationByExternal(ctx, ev.Installation)
	if errx.IsType(err, errx.TypeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch ev.Action {
	case actionDeleted:
		// the App was uninstalled on GitHub: like Unlink, by no one
		return s.removeInstallation(ctx, inst, identity.UserID{}, source.ReasonUninstalled)
	case source.ActionSuspend, source.ActionUnsuspend, actionPermissionsAccepted, actionAdded, actionRemoved:
		return s.refreshInstallation(ctx, inst, ev)
	}
	return nil
}

// refreshInstallation re-reads the installation from the provider (what the
// action implies when it cannot be read) and publishes installation_changed;
// repositories taken away also disconnect their projects.
func (s *Service) refreshInstallation(ctx context.Context, inst source.Installation, ev source.InstallationEvent) error {
	updated := inst
	info, err := s.provider.Installation(ctx, inst.ExternalID)
	if err != nil {
		slog.Warn("could not refresh an installation after its webhook", "installation", inst.ID,
			"action", ev.Action, "error", err)
	} else {
		updated.Account, updated.AccountType = info.Account, info.AccountType
		updated.AllRepositories, updated.Suspended = info.AllRepositories, info.Suspended
	}
	switch ev.Action {
	case source.ActionSuspend:
		updated.Suspended = true
	case source.ActionUnsuspend:
		updated.Suspended = false
	}
	updated.LinkedBy = identity.UserID{} // zero keeps the stored value
	saved, err := s.repo.UpsertInstallation(ctx, updated)
	if err != nil {
		return err
	}
	published := []event.Event{source.NewInstallationChanged(saved, ev.Action)}
	if ev.Action == actionRemoved && len(ev.Removed) > 0 { // an empty list would mean every repository
		gone, err := s.repo.DeleteConnectionsOfInstallation(ctx, saved.Organization, saved.ID, ev.Removed)
		if err != nil {
			return err
		}
		for _, c := range gone {
			published = append(published, source.NewDisconnected(c, identity.UserID{}, source.ReasonRepositoryRemoved))
		}
	}
	return s.events.Publish(ctx, published...)
}

// ---- reconcile ----

// Reconcile polls the branches of the connections not checked for
// ReconcileEvery and queues the ones that moved.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	if !s.provider.Enabled() {
		return 0, nil
	}
	now := s.opts.Now()
	conns, err := s.repo.ListConnectionsToReconcile(ctx, now.Add(-source.ReconcileEvery), reconcileBatch)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, c := range conns {
		moved, err := s.reconcile(ctx, c, now)
		if moved {
			queued++
		}
		if err != nil {
			if providerDown(err) {
				return queued, err // the next round tries again
			}
			slog.Warn("could not reconcile a connection", "connection", c.ID, "error", err)
		}
	}
	return queued, nil
}

// providerDown: the provider did not answer (as opposed to answering "no").
func providerDown(err error) bool {
	var e *errx.Error
	return errors.As(err, &e) && (e.Type == errx.TypeExternal || e.Code == source.CodeProviderDown)
}

func hasCode(err error, code string) bool {
	var e *errx.Error
	return errors.As(err, &e) && e.Code == code
}

// reconcile checks one connection; moved reports a commit was queued.
func (s *Service) reconcile(ctx context.Context, c source.Connection, now time.Time) (moved bool, err error) {
	inst, err := s.repo.FindInstallation(ctx, c.Organization, c.Installation)
	if errx.IsType(err, errx.TypeNotFound) {
		return false, s.repo.ObserveHead(ctx, c.ID, c.ETag, now)
	}
	if err != nil {
		return false, err
	}
	if inst.Suspended {
		return false, nil
	}
	head, etag, changed, err := s.provider.Head(ctx, inst.ExternalID, c.RepositoryID, c.Branch, c.ETag)
	switch {
	case providerDown(err):
		return false, err
	case errx.IsType(err, errx.TypeNotFound) || hasCode(err, source.CodeNoAccess):
		// the repository (no access) or the branch is gone: look again in a while
		return false, s.repo.ObserveHead(ctx, c.ID, c.ETag, now)
	case err != nil:
		return false, err // logged by the round; checked again next time
	case !changed || head.SHA == c.LastCommit:
		return false, s.repo.ObserveHead(ctx, c.ID, etag, now)
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		locked, err := s.repo.LockConnection(ctx, c.Organization, c.Project)
		if errx.IsType(err, errx.TypeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if locked.ID != c.ID || locked.Branch != c.Branch || locked.RepositoryID != c.RepositoryID {
			return nil // reconnected meanwhile: its check starts over
		}
		observed := etag
		if locked.LastCommit != head.SHA {
			p, err := s.projects.Find(ctx, c.Organization, c.Project)
			switch {
			case errx.IsType(err, errx.TypeNotFound):
				return nil
			case err != nil:
				return err
			case p.Archived():
				observed = c.ETag // look at it again once the app is back
			default:
				push, err := s.queue(ctx, locked, head, source.TriggerReconcile, identity.UserID{}, github(""))
				if err != nil {
					return err
				}
				if err := s.events.Publish(ctx, source.NewPushReceived(push)); err != nil {
					return err
				}
				moved = true
			}
		}
		return s.repo.ObserveHead(ctx, c.ID, observed, now)
	})
	return moved && err == nil, err
}
