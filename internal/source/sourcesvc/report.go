package sourcesvc

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// Report shows a push's progress as a commit status. It is best effort: a
// status that is out of date by the time the provider answers again is worth
// nothing, so provider errors are logged and swallowed (the outbox would
// otherwise retry them); only database errors are returned.
func (s *Service) Report(ctx context.Context, organization identity.OrganizationID, input source.Report) error {
	var (
		push source.Push
		err  error
	)
	if !input.Push.IsZero() {
		push, err = s.repo.FindPush(ctx, organization, input.Push)
	} else {
		push, err = s.repo.FindPushByDeployment(ctx, organization, input.Deployment)
	}
	if errx.IsType(err, errx.TypeNotFound) {
		return nil // not published from git
	}
	if err != nil {
		return err
	}
	c, err := s.repo.FindConnection(ctx, organization, push.Project)
	if errx.IsType(err, errx.TypeNotFound) || (err == nil && c.ID != push.Connection) {
		return nil // disconnected, or connected to something else since
	}
	if err != nil {
		return err
	}
	inst, err := s.repo.FindInstallation(ctx, organization, c.Installation)
	if errx.IsType(err, errx.TypeNotFound) || (err == nil && inst.Suspended) {
		return nil
	}
	if err != nil {
		return err
	}
	status := input.Status
	if s.opts.ConsoleURL != "" {
		status.TargetURL = strings.TrimRight(s.opts.ConsoleURL, "/") + "/apps/" + push.Project.String()
	}
	if err := s.provider.SetStatus(ctx, inst.ExternalID, c.RepositoryID, push.Git.Commit, status.Truncated()); err != nil {
		slog.Warn("could not set a commit status", "push", push.ID, "commit", push.Git.Commit,
			"state", status.State, "error", err)
	}
	return nil
}
