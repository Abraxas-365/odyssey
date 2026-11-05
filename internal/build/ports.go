package build

import (
	"context"
	"io"
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Commands are the write operations, implemented by buildsvc.Service.
type Commands interface {
	// Upload streams a source archive to storage, validating it on the way
	// (a bad archive is a Validation error and leaves nothing stored).
	Upload(ctx context.Context, organization identity.OrganizationID, project project.ID, input Upload, archive io.Reader) (Source, error)
	// Start builds a source; asking again with the same build ID returns the
	// same build. It joins the caller's transaction.
	Start(ctx context.Context, organization identity.OrganizationID, input Start) (Build, error)
	// Cancel stops a build nobody needs; a finished build is left as is.
	// It joins the caller's transaction.
	Cancel(ctx context.Context, organization identity.OrganizationID, build ID, input Cancel) error
	// Discard drops at once a source no build used (the deployment that
	// uploaded it could not start). A source a build used is a Conflict: the
	// sweep keeps it for its retention.
	Discard(ctx context.Context, organization identity.OrganizationID, source SourceID) error
	// Sweep expires sources older than their retention that no unfinished
	// build needs, deleting their bytes (a scheduled task).
	Sweep(ctx context.Context) (int, error)
}

// Queries are the read operations, implemented by buildsvc.Service.
type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, build ID) (Build, error)
	// Scans is the recorded scan of each of the builds (at most MaxScans), in
	// one query: what the inventory shows. A build with no scan recorded is
	// absent. It changes nothing.
	Scans(ctx context.Context, organization identity.OrganizationID, builds []ID) ([]BuildScan, error)
	FindSource(ctx context.Context, organization identity.OrganizationID, source SourceID) (Source, error)
	// Logs pages a build's log: live from the runner while it runs, from the
	// archive once it ended.
	Logs(ctx context.Context, organization identity.OrganizationID, build ID, query LogQuery) (LogPage, error)
}

// Workflows are the definitions the composition root registers.
type Workflows interface {
	Workflows() []workflow.Definition
}

// Runner builds images: the agent of the environment's cluster (buildfleet,
// phase 1b-2) or buildfake. Every method is idempotent.
type Runner interface {
	// Dispatch hands the build to the cluster that will run it.
	Dispatch(ctx context.Context, job Job) error
	// Observe reports the build (PhasePending when the runner has no news).
	Observe(ctx context.Context, build Build) (Observation, error)
	// Logs returns the lines after seq, in order, at most limit.
	Logs(ctx context.Context, build Build, after int64, limit int) ([]LogLine, error)
	// Cancel stops the build if it is still running.
	Cancel(ctx context.Context, build Build) error
	// Forget drops what the runner keeps of a build whose log is archived.
	Forget(ctx context.Context, build Build) error
	// ObserveScan says where the scan of a succeeded build's image is
	// (ScanUnsupported for a runner or cluster that does not scan).
	ObserveScan(ctx context.Context, build Build) (ScanObservation, error)
}

// Repository is the persistence contract; only the service depends on it.
// Every statement filters by organization (another tenant's row is NotFound).
type Repository interface {
	CreateSource(ctx context.Context, source Source) error
	FindSource(ctx context.Context, organization identity.OrganizationID, source SourceID) (Source, error)
	// LockSource holds the source (FOR SHARE) so a sweep cannot expire it
	// while a build of it is being created.
	LockSource(ctx context.Context, organization identity.OrganizationID, source SourceID) (Source, error)
	// LockExpirable locks (SKIP LOCKED) up to limit unexpired sources that
	// no unfinished build needs: built before cutoff, or never built and
	// uploaded before orphanCutoff (the deployment that uploaded it failed).
	LockExpirable(ctx context.Context, cutoff, orphanCutoff time.Time, limit int) ([]Source, error)
	// Expire marks a source expired (its bytes are about to be deleted).
	Expire(ctx context.Context, organization identity.OrganizationID, source SourceID) error
	// ExpireUnused expires a source no build used and returns it (Conflict
	// when a build used it or it already expired).
	ExpireUnused(ctx context.Context, organization identity.OrganizationID, source SourceID) (Source, error)
	// LockQuota serialises the uploads of an organization until the
	// transaction ends, so two of them cannot both fit in the same room.
	LockQuota(ctx context.Context, organization identity.OrganizationID) error
	// Usage is what the organization's sources take.
	Usage(ctx context.Context, organization identity.OrganizationID) (Usage, error)
	// LockEvictable locks (SKIP LOCKED), oldest first, up to limit of the
	// organization's evictable sources (see Usage).
	LockEvictable(ctx context.Context, organization identity.OrganizationID, limit int) ([]Source, error)
	// ListUndeleted are expired sources whose bytes are still stored.
	ListUndeleted(ctx context.Context, limit int) ([]Source, error)
	// ForgetSourceBytes records that an expired source's bytes are gone.
	ForgetSourceBytes(ctx context.Context, organization identity.OrganizationID, source SourceID) error

	CreateBuild(ctx context.Context, build Build) error
	FindBuild(ctx context.Context, organization identity.OrganizationID, build ID) (Build, error)
	// ListScans is the recorded scan of each of the builds (at most MaxScans)
	// in one query; a build without one (not scanned yet, never, or not the
	// organization's) is absent.
	ListScans(ctx context.Context, organization identity.OrganizationID, builds []ID) ([]BuildScan, error)
	// FindBuildForUpdate locks the row until the transaction ends.
	FindBuildForUpdate(ctx context.Context, organization identity.OrganizationID, build ID) (Build, error)
	// MarkRunning moves a queued build to running (no-op otherwise).
	MarkRunning(ctx context.Context, organization identity.OrganizationID, build ID) error
	// Finish moves an unfinished build to a final status.
	Finish(ctx context.Context, organization identity.OrganizationID, build ID, status Status, image string, failure *Failure) error
	// SetLogs records where a finished build's log is archived, with its
	// index (where to start reading for a seq).
	SetLogs(ctx context.Context, organization identity.OrganizationID, build ID, key string, index LogIndex) error
	// SetScan records the scan of a succeeded build, once: false when it
	// already has one (NotFound when the build is unknown or did not
	// succeed).
	SetScan(ctx context.Context, organization identity.OrganizationID, build ID, scan Scan) (bool, error)
}
