// Package buildsvc implements build.Commands and build.Queries and the
// build.run workflow that follows a build in the cluster.
//
// The build row is the truth: Cancel only marks it, and the run (which never
// holds the row while it talks to the runner) reacts at its next step.
package buildsvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/archive"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
)

// Options tune the service; zero values take the defaults.
type Options struct {
	// Retention is how long a built source is kept (default 7 days).
	Retention time.Duration
	// OrphanRetention is how long a source no build used is kept (default 24 h).
	OrphanRetention time.Duration
	// PollEvery is how often the wait step asks the runner (default 2 s).
	PollEvery time.Duration
	// Timeout bounds a build (default build.Timeout).
	Timeout time.Duration
	// ScanTimeout bounds the wait for a scan (default build.ScanTimeout).
	ScanTimeout time.Duration
	// Limits bound uploaded archives (default archive.Default()).
	Limits archive.Limits
	// SourceQuota bounds the bytes of an organization's unexpired sources
	// (default 10 GiB). A full quota expires the oldest built sources
	// early; when none can go, uploads are refused (SOURCE_QUOTA).
	SourceQuota int64
}

// defaultSourceQuota is 10 GiB of compressed sources per organization.
const defaultSourceQuota = 10 << 30

type Service struct {
	repo     build.Repository
	projects project.Queries
	runner   build.Runner
	blobs    blob.Store
	engine   workflow.Engine
	tx       txn.Runner
	events   event.Publisher
	opts     Options
}

func New(repo build.Repository, projects project.Queries, runner build.Runner, blobs blob.Store, engine workflow.Engine,
	tx txn.Runner, events event.Publisher, opts Options) *Service {
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	if opts.OrphanRetention <= 0 {
		opts.OrphanRetention = 24 * time.Hour
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 2 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = build.Timeout
	}
	if opts.Limits == (archive.Limits{}) {
		opts.Limits = archive.Default()
	}
	if opts.ScanTimeout <= 0 {
		opts.ScanTimeout = build.ScanTimeout
	}
	if opts.SourceQuota <= 0 {
		opts.SourceQuota = defaultSourceQuota
	}
	return &Service{repo: repo, projects: projects, runner: runner, blobs: blobs, engine: engine, tx: tx, events: events, opts: opts}
}

var (
	_ build.Commands  = (*Service)(nil)
	_ build.Queries   = (*Service)(nil)
	_ build.Workflows = (*Service)(nil)
)

// sourceKey is where a source's bytes live.
func sourceKey(s build.Source) string {
	return blob.TenantKey(s.Organization, "sources", s.ID.String()+".tar.gz")
}

// logsKey is where a finished build's log is archived.
func logsKey(b build.Build) string {
	return blob.TenantKey(b.Organization, "builds", b.ID.String()+".log.jsonl")
}

// Upload verifies the archive while streaming it to storage: only a valid
// archive is stored, and its row is written after its bytes, so a row always
// points at bytes (bytes without a row — a crash in between — are harmless
// garbage under a fresh key).
func (s *Service) Upload(ctx context.Context, organization identity.OrganizationID, id project.ID, input build.Upload, data io.Reader) (build.Source, error) {
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return build.Source{}, err
	}
	p, err := s.projects.Find(ctx, organization, id)
	if err != nil {
		return build.Source{}, err
	}
	if p.Archived() {
		return build.Source{}, errx.Business("the app is archived: unarchive it to publish").WithCode(project.CodeArchived)
	}
	src := build.Source{ID: build.NewSourceID(), Organization: organization, Project: id, Name: input.Name, UploadedBy: input.UploadedBy, Git: input.Git}
	src.Key = sourceKey(src)
	// Refused before reading the body when even dropping every evictable
	// source would not leave room (checked again, exactly, below).
	if u, err := s.repo.Usage(ctx, organization); err != nil {
		return build.Source{}, err
	} else if u.Stored-u.Evictable >= s.opts.SourceQuota {
		return build.Source{}, quotaFull()
	}
	v := archive.NewVerifier(data, s.opts.Limits)
	defer v.Close()
	if err := s.blobs.Put(ctx, src.Key, v, blob.PutOptions{ContentType: "application/gzip",
		Metadata: map[string]string{"organization": organization.String(), "project": id.String()}}); err != nil {
		return build.Source{}, err
	}
	sum := v.Summary()
	src.Size, src.SHA256, src.Files, src.UnpackedSize = sum.Size, sum.SHA256, sum.Files, sum.UnpackedSize
	var evicted []build.Source
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if evicted, err = s.makeRoom(ctx, organization, src.Size, false); err != nil {
			return err
		}
		if err := s.repo.CreateSource(ctx, src); err != nil {
			return err
		}
		return s.events.Publish(ctx, build.NewSourceUploaded(organization, input.UploadedBy, src, build.Names{Project: p.Name}))
	})
	if err != nil {
		if derr := s.blobs.Delete(context.WithoutCancel(ctx), src.Key); derr != nil {
			slog.Warn("could not delete the bytes of an unrecorded source", "key", src.Key, "error", derr)
		}
		return build.Source{}, err
	}
	s.dropBytes(ctx, evicted)
	return s.repo.FindSource(ctx, organization, src.ID)
}

func quotaFull() error {
	return errx.Business("the organization's uploaded sources fill its quota and none can be dropped yet").
		WithCode(build.CodeSourceQuota)
}

// makeRoom keeps the organization's sources within its quota with size more:
// it expires the oldest sources already built (their images stay) until
// they fit, or refuses with SOURCE_QUOTA. Uploads of an organization take
// turns here (LockQuota), so two cannot both count the same room.
//
// admitted is for a folder cut from an upload (Derive): the quota admitted
// that upload, and its folders together weigh about what it does, so they
// make what room there is and are stored even over the quota (the next
// upload makes the room back). Refusing would fail a deployment whose
// upload was accepted, for room nothing frees while its builds wait.
func (s *Service) makeRoom(ctx context.Context, organization identity.OrganizationID, size int64, admitted bool) ([]build.Source, error) {
	if err := s.repo.LockQuota(ctx, organization); err != nil {
		return nil, err
	}
	u, err := s.repo.Usage(ctx, organization)
	if err != nil {
		return nil, err
	}
	need := u.Stored + size - s.opts.SourceQuota
	if need <= 0 {
		return nil, nil
	}
	if need > u.Evictable && !admitted {
		return nil, quotaFull()
	}
	var evicted []build.Source
	for need > 0 {
		batch, err := s.repo.LockEvictable(ctx, organization, sweepBatch)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 { // the rest is held by a sweep expiring it
			if admitted {
				return evicted, nil
			}
			return nil, quotaFull()
		}
		for _, src := range batch {
			if need <= 0 {
				break
			}
			if err := s.repo.Expire(ctx, organization, src.ID); err != nil {
				return nil, err
			}
			if err := s.events.Publish(ctx, build.NewSourceExpired(organization, src)); err != nil {
				return nil, err
			}
			evicted = append(evicted, src)
			need -= src.Size
		}
	}
	return evicted, nil
}

// dropBytes deletes the bytes of sources just expired. It is best effort:
// what it cannot delete, the sweep does.
func (s *Service) dropBytes(ctx context.Context, sources []build.Source) {
	ctx = context.WithoutCancel(ctx)
	for _, src := range sources {
		if err := s.blobs.Delete(ctx, src.Key); err != nil {
			slog.Warn("could not delete the bytes of an expired source; the sweep will", "source", src.ID, "error", err)
			continue
		}
		if err := s.repo.ForgetSourceBytes(ctx, src.Organization, src.ID); err != nil {
			slog.Warn("could not record a source's bytes as deleted; the sweep will", "source", src.ID, "error", err)
		}
	}
}

// Discard expires a source no build used and deletes its bytes now, instead
// of leaving them to the orphan sweep.
func (s *Service) Discard(ctx context.Context, organization identity.OrganizationID, id build.SourceID) error {
	var src build.Source
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		if src, err = s.repo.ExpireUnused(ctx, organization, id); err != nil {
			return err
		}
		return s.events.Publish(ctx, build.NewSourceExpired(organization, src))
	})
	if err != nil {
		return err
	}
	s.dropBytes(ctx, []build.Source{src})
	return nil
}

// Derive cuts the folder root out of an uploaded source into a source of
// its own (stored like an upload, counted in the quota), once per parent
// and folder: asking again returns the same source while it lasts. The
// bytes stream from the parent's blob to the new one, outside any
// transaction.
func (s *Service) Derive(ctx context.Context, organization identity.OrganizationID, input build.Derive) (build.Source, error) {
	if err := input.Validate(); err != nil {
		return build.Source{}, err
	}
	parent, err := s.repo.FindSource(ctx, organization, input.Parent)
	if err != nil {
		return build.Source{}, err
	}
	if parent.Parent != nil {
		return build.Source{}, errx.Validation("only an uploaded source can be cut into folders")
	}
	if found, err := s.repo.FindDerived(ctx, organization, parent.ID, input.Root); err == nil {
		return found, nil
	} else if !errx.IsType(err, errx.TypeNotFound) {
		return build.Source{}, err
	}
	if parent.Expired() {
		return build.Source{}, build.Refused(build.CodeSourceExpired, "the source was swept before it was built")
	}
	p, err := s.projects.Find(ctx, organization, parent.Project)
	if err != nil {
		return build.Source{}, err
	}
	src := build.Source{ID: build.NewSourceID(), Organization: organization, Project: parent.Project, Name: parent.Name,
		UploadedBy: input.RequestedBy, Git: parent.Git, Parent: &parent.ID, Root: input.Root}
	src.Key = sourceKey(src)
	sum, err := s.cut(ctx, parent, src, input.Root)
	if err != nil {
		return build.Source{}, err
	}
	src.Size, src.SHA256, src.Files, src.UnpackedSize = sum.Size, sum.SHA256, sum.Files, sum.UnpackedSize
	var evicted []build.Source
	raced := false
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// Held so a sweep cannot expire the parent under its new folder.
		held, err := s.repo.LockSource(ctx, organization, parent.ID)
		if err != nil {
			return err
		}
		if held.Expired() {
			return build.Refused(build.CodeSourceExpired, "the source was swept before it was built")
		}
		if _, err := s.repo.FindDerived(ctx, organization, parent.ID, input.Root); err == nil {
			raced = true // another deploy cut the same folder first
			return nil
		} else if !errx.IsType(err, errx.TypeNotFound) {
			return err
		}
		if evicted, err = s.makeRoom(ctx, organization, src.Size, true); err != nil {
			return err
		}
		if err := s.repo.CreateSource(ctx, src); err != nil {
			return err
		}
		return s.events.Publish(ctx, build.NewSourceDerived(organization, input.RequestedBy, src, build.Names{Project: p.Name}))
	})
	if err != nil && errx.IsType(err, errx.TypeConflict) {
		raced, err = true, nil // the unique folder index: same as above, seen late
	}
	if err != nil || raced {
		if derr := s.blobs.Delete(context.WithoutCancel(ctx), src.Key); derr != nil {
			slog.Warn("could not delete the bytes of an unrecorded source", "key", src.Key, "error", derr)
		}
		if err != nil {
			return build.Source{}, err
		}
		return s.repo.FindDerived(ctx, organization, parent.ID, input.Root)
	}
	s.dropBytes(ctx, evicted)
	return s.repo.FindSource(ctx, organization, src.ID)
}

// cut streams the folder root of parent's bytes into src's key.
func (s *Service) cut(ctx context.Context, parent, src build.Source, root string) (archive.Summary, error) {
	in, err := s.blobs.Open(ctx, parent.Key)
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return archive.Summary{}, build.Refused(build.CodeSourceExpired, "the source's bytes are gone")
		}
		return archive.Summary{}, err
	}
	defer in.Close()
	pr, pw := io.Pipe()
	type result struct {
		sum archive.Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := archive.Subtree(in, pw, root, s.opts.Limits)
		pw.CloseWithError(err) // nil: Put reads io.EOF
		done <- result{sum, err}
	}()
	perr := s.blobs.Put(ctx, src.Key, pr, blob.PutOptions{ContentType: "application/gzip",
		Metadata: map[string]string{"organization": src.Organization.String(), "project": src.Project.String()}})
	pr.CloseWithError(io.ErrClosedPipe) // unblocks the cut when Put gave up early
	r := <-done
	if r.err == nil && perr == nil {
		return r.sum, nil
	}
	if derr := s.blobs.Delete(context.WithoutCancel(ctx), src.Key); derr != nil && !errx.IsType(derr, errx.TypeNotFound) {
		slog.Warn("could not delete the bytes of an unrecorded source", "key", src.Key, "error", derr)
	}
	if r.err != nil && !errors.Is(r.err, io.ErrClosedPipe) {
		return archive.Summary{}, r.err // the cut's own refusal explains it best
	}
	return archive.Summary{}, perr
}

// Start creates the build and its run; with an ID that exists it returns
// that build (the caller's workflow step may run more than once).
func (s *Service) Start(ctx context.Context, organization identity.OrganizationID, input build.Start) (build.Build, error) {
	if err := input.Validate(); err != nil {
		return build.Build{}, err
	}
	var out build.Build
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		existing, err := s.repo.FindBuild(ctx, organization, input.ID)
		if err == nil {
			if existing.Source != input.Source || existing.Environment != input.Environment {
				return errx.Conflict("a different build exists with this id")
			}
			out = existing
			return nil
		}
		if !errx.IsType(err, errx.TypeNotFound) {
			return err
		}
		p, err := s.projects.Find(ctx, organization, input.Project)
		if err != nil {
			return err
		}
		if _, err := s.projects.FindEnvironment(ctx, organization, input.Project, input.Environment); err != nil {
			return err
		}
		src, err := s.repo.LockSource(ctx, organization, input.Source)
		if err != nil {
			return err
		}
		if src.Project != input.Project {
			return errx.NotFound("source not found")
		}
		if src.Expired() {
			return build.Refused(build.CodeSourceExpired, "the source was swept before it was built")
		}
		b := build.Build{ID: input.ID, Organization: organization, Project: input.Project, Environment: input.Environment,
			Source: src.ID, Status: build.StatusQueued, RequestedBy: input.RequestedBy}
		run, err := s.engine.Start(ctx, workflow.Start{Workflow: build.WorkflowRun, Organization: organization,
			Actor: input.RequestedBy, Subject: event.Subject(build.SubjectKind, b.ID),
			Input: build.RunInput{Build: b.ID}, IdempotencyKey: b.ID.String()})
		if err != nil {
			return err
		}
		b.Run = run
		if err := s.repo.CreateBuild(ctx, b); err != nil {
			return err
		}
		if err := s.events.Publish(ctx, build.NewStarted(organization, input.RequestedBy, b, build.Names{Project: p.Name})); err != nil {
			return err
		}
		out, err = s.repo.FindBuild(ctx, organization, b.ID)
		return err
	})
	return out, err
}

// Cancel marks the build cancelled. Its run notices at its next step and
// stops the build in the runner (durably, with retries), then archives what
// it printed. It joins the caller's transaction.
func (s *Service) Cancel(ctx context.Context, organization identity.OrganizationID, id build.ID, input build.Cancel) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		b, err := s.repo.FindBuildForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if b.Status.Finished() {
			return nil
		}
		if err := s.repo.Finish(ctx, organization, id, build.StatusCancelled, "", nil); err != nil {
			return err
		}
		p, err := s.projects.Find(ctx, organization, b.Project)
		if err != nil {
			return err
		}
		return s.events.Publish(ctx, build.NewCancelled(organization, input.CancelledBy, b, build.Names{Project: p.Name}))
	})
}

// sweepBatch bounds the sources one sweep handles.
const sweepBatch = 100

// Sweep expires sources past their retention, then deletes the bytes of
// every expired source (also those a crashed sweep left behind).
func (s *Service) Sweep(ctx context.Context) (int, error) {
	now := time.Now()
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		sources, err := s.repo.LockExpirable(ctx, now.Add(-s.opts.Retention), now.Add(-s.opts.OrphanRetention), sweepBatch)
		if err != nil {
			return err
		}
		for _, src := range sources {
			if err := s.repo.Expire(ctx, src.Organization, src.ID); err != nil {
				return err
			}
			if err := s.events.Publish(ctx, build.NewSourceExpired(src.Organization, src)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	undeleted, err := s.repo.ListUndeleted(ctx, sweepBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, src := range undeleted {
		if err := s.blobs.Delete(ctx, src.Key); err != nil {
			return n, err
		}
		if err := s.repo.ForgetSourceBytes(ctx, src.Organization, src.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---- queries ----

func (s *Service) Find(ctx context.Context, organization identity.OrganizationID, id build.ID) (build.Build, error) {
	return s.repo.FindBuild(ctx, organization, id)
}

func (s *Service) Scans(ctx context.Context, organization identity.OrganizationID, ids []build.ID) ([]build.BuildScan, error) {
	if len(ids) == 0 {
		return []build.BuildScan{}, nil
	}
	if len(ids) > build.MaxScans {
		return nil, errx.Validation("too many builds in one read")
	}
	return s.repo.ListScans(ctx, organization, ids)
}

func (s *Service) FindSource(ctx context.Context, organization identity.OrganizationID, id build.SourceID) (build.Source, error) {
	return s.repo.FindSource(ctx, organization, id)
}
