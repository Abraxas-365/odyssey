package fleetsvc

import (
	"context"
	"strconv"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// ---- Builds -------------------------------------------------------------

// DispatchBuild places the build like Apply places an app, then stores it
// for that cluster and signals its gateway, in one transaction. The image
// goes to the registry the cluster's builder announced, under the
// organization and project; the cache next to it.
func (s *Service) DispatchBuild(ctx context.Context, b fleet.DesiredBuild) error {
	if err := b.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := s.repo.FindBuild(ctx, b.Organization, b.ID); err == nil {
			return nil // already sent (a retried dispatch)
		} else if !errx.IsType(err, errx.TypeNotFound) {
			return err
		}
		c, placed, err := s.placement(ctx, b.Organization, b.Project, b.Environment)
		if err != nil {
			return err
		}
		if c.Agent.Capabilities.Builder == nil {
			return errx.Business("El clúster " + c.Name + " todavía no puede construir apps. Pide a TI que instale el constructor del agente.").
				WithCode(fleet.CodeNoBuilder)
		}
		repo := c.Agent.Capabilities.Builder.Repository(b.Organization, b.Project)
		b.Cluster, b.ImageRepository, b.CacheRef = c.ID, repo, repo+":buildcache"
		changed, err := s.repo.PutBuild(ctx, b)
		if err != nil || !changed {
			return err
		}
		return s.events.Publish(ctx, fleet.NewBuildSent(b.Organization, c, b, placed))
	})
}

func (s *Service) CancelBuild(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		b, changed, err := s.repo.CancelBuild(ctx, organization, id)
		if errx.IsType(err, errx.TypeNotFound) || (err == nil && !changed) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewBuildReleased(organization, b))
	})
}

func (s *Service) BuildStatus(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) (fleet.BuildStatus, error) {
	st, err := s.repo.FindBuildStatus(ctx, organization, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.BuildStatus{Build: id, Phase: fleet.BuildPending}, nil
	}
	return st, err
}

// BuildLogs pages the stored lines; a truncated log ends with a line
// saying so, once every stored line was read.
func (s *Service) BuildLogs(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID, after int64, limit int) (fleet.LogPage, error) {
	page, err := s.repo.ListBuildLog(ctx, organization, id, after, limit)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.LogPage{Lines: []fleet.LogLine{}}, nil
	}
	if err != nil {
		return fleet.LogPage{}, err
	}
	// the build's log ends with the cut line, once every stored line was
	// read (not when nothing is stored: it was archived, cut line included)
	if page.Truncated && page.LastSeq > 0 && len(page.Lines) < limit && after <= page.LastSeq {
		page.Lines = append(page.Lines, fleet.LogLine{Seq: page.LastSeq + 1, Stream: "system", Text: fleet.LogCutText})
	}
	return page, nil
}

func (s *Service) ForgetBuildLogs(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) error {
	return s.repo.ObserveLogsArchived(ctx, organization, id)
}

func (s *Service) DesiredBuilds(ctx context.Context, agent fleet.AgentRef) ([]fleet.DesiredBuild, error) {
	return s.repo.ListBuilds(ctx, agent.Organization, agent.Cluster, s.opts.Now().Add(-fleet.BuildResendWithin))
}

func (s *Service) DesiredBuild(ctx context.Context, agent fleet.AgentRef, id fleet.BuildID) (fleet.DesiredBuild, bool, error) {
	b, err := s.repo.FindBuild(ctx, agent.Organization, id)
	if errx.IsType(err, errx.TypeNotFound) || (err == nil && b.Cluster != agent.Cluster) {
		return fleet.DesiredBuild{}, false, nil
	}
	return b, err == nil, err
}

func (s *Service) BuildSource(ctx context.Context, agent fleet.AgentRef, id fleet.BuildID) (fleet.DesiredBuild, error) {
	b, err := s.repo.FindBuild(ctx, agent.Organization, id)
	if err != nil {
		return fleet.DesiredBuild{}, err
	}
	// another cluster's build is as unknown as a missing one
	if b.Cluster != agent.Cluster || !b.Wanted(s.opts.Now()) {
		return fleet.DesiredBuild{}, errx.NotFound("no build of this cluster wants this source")
	}
	return b, nil
}

// ReportBuilds stores what the agent says of its builds. Invalid statuses
// are dropped (and answered with a validation error) while the valid ones
// are kept, like Report.
func (s *Service) ReportBuilds(ctx context.Context, agent fleet.AgentRef, session fleet.Session, statuses []fleet.BuildStatus) error {
	if len(statuses) > fleet.MaxReportBuilds {
		return errx.Validation("too many builds in one report")
	}
	valid := make([]fleet.BuildStatus, 0, len(statuses))
	bad := 0
	for _, st := range statuses {
		if err := st.Validate(); err != nil {
			bad++
			continue
		}
		valid = append(valid, st.Clean())
	}
	live, err := s.repo.ObserveBuilds(ctx, agent.Organization, agent.Cluster, session, valid)
	if err != nil {
		return err
	}
	if !live {
		return s.lost(ctx, agent)
	}
	if bad > 0 {
		return errx.Validation(strconv.Itoa(bad) + " reported build statuses were not valid and were dropped")
	}
	return nil
}

// ReportScans stores what the agent says of its scans. Invalid ones are
// dropped (and answered with a validation error) while the valid ones are
// kept, like ReportBuilds.
func (s *Service) ReportScans(ctx context.Context, agent fleet.AgentRef, session fleet.Session, scans []fleet.ScanStatus) error {
	if len(scans) > fleet.MaxReportScans {
		return errx.Validation("too many scans in one report")
	}
	valid := make([]fleet.ScanStatus, 0, len(scans))
	bad := 0
	for _, sc := range scans {
		if err := sc.Validate(); err != nil {
			bad++
			continue
		}
		valid = append(valid, sc.Clean())
	}
	live, err := s.repo.ObserveScans(ctx, agent.Organization, agent.Cluster, session, valid)
	if err != nil {
		return err
	}
	if !live {
		return s.lost(ctx, agent)
	}
	if bad > 0 {
		return errx.Validation(strconv.Itoa(bad) + " reported scans were not valid and were dropped")
	}
	return nil
}

// BuildScan answers where a build's scan is: the stored outcome; otherwise
// pending while the build's cluster lists CapabilityScan, unsupported when it
// does not (or the build is unknown: there is nothing to wait for).
func (s *Service) BuildScan(ctx context.Context, organization identity.OrganizationID, id fleet.BuildID) (fleet.BuildScan, error) {
	sc, err := s.repo.FindBuildScan(ctx, organization, id)
	if err == nil {
		return fleet.BuildScan{Phase: fleet.ScanDone, Status: sc}, nil
	}
	if !errx.IsType(err, errx.TypeNotFound) {
		return fleet.BuildScan{}, err
	}
	b, err := s.repo.FindBuild(ctx, organization, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.BuildScan{Phase: fleet.ScanUnsupported}, nil
	}
	if err != nil {
		return fleet.BuildScan{}, err
	}
	c, err := s.repo.FindCluster(ctx, organization, b.Cluster)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.BuildScan{Phase: fleet.ScanUnsupported}, nil
	}
	if err != nil {
		return fleet.BuildScan{}, err
	}
	if !c.Agent.Capabilities.CanScan() {
		return fleet.BuildScan{Phase: fleet.ScanUnsupported}, nil
	}
	return fleet.BuildScan{Phase: fleet.ScanPending}, nil
}

func (s *Service) LogBuild(ctx context.Context, agent fleet.AgentRef, session fleet.Session, log fleet.BuildLog) error {
	if err := log.Validate(); err != nil {
		return err
	}
	live, err := s.repo.ObserveBuildLog(ctx, agent.Organization, agent.Cluster, session, log.Clean(), fleet.MaxBuildLogBytes, fleet.LogLineCost)
	if err != nil {
		return err
	}
	if !live {
		return s.lost(ctx, agent)
	}
	return nil
}
