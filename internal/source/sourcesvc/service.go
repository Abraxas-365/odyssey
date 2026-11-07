// Package sourcesvc implements source.Commands and source.Queries and the
// import workflow that turns a pushed commit into a deployment.
//
// A commit to publish is queued (a Push) under the connection's lock, with
// its source.import run, in one transaction. The run downloads the commit's
// tarball with a short-lived installation token, repacks it (only the app's
// folder, no git metadata) straight into the build domain's upload, and
// starts a production deployment of it through the Deployer port. The
// cluster never sees a GitHub credential.
//
// Locks are taken in the order connection → push → release's (project,
// environment state, deployment), so the deploy step, Connect/Publish and
// release's own paths never deadlock.
package sourcesvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

// Options tune the service; zero values take the defaults.
type Options struct {
	// StateKey signs the install state (required when the provider is enabled).
	StateKey []byte
	// Limits bound an imported commit (default: the platform's source limits).
	Limits srcarchive.Limits
	// ImportAttempts bounds the import step's tries on transient errors
	// (default 6: about a minute of backoff while GitHub hiccups).
	ImportAttempts int
	// Now is the clock (tests).
	Now func() time.Time
	// ConsoleURL is the console's base URL: commit statuses link to
	// <ConsoleURL>/apps/<project id> (no link when empty).
	ConsoleURL string
	// Assistants are the clients IAMKit registered for the template
	// assistants (IAMKIT_MCP_CLIENT_<KEY>_ID), to name them in a
	// deployment's requested_via.
	Assistants assistant.Registered
}

type Service struct {
	repo     source.Repository
	provider source.GitProvider
	builder  source.Builder
	deployer source.Deployer
	projects project.Queries
	engine   workflow.Engine
	tx       txn.Runner
	events   event.Publisher
	opts     Options
}

func New(repo source.Repository, provider source.GitProvider, builder source.Builder, deployer source.Deployer,
	projects project.Queries, engine workflow.Engine, tx txn.Runner, events event.Publisher, opts Options) *Service {
	if opts.Limits == (srcarchive.Limits{}) {
		opts.Limits = srcarchive.Limits{Compressed: build.MaxSourceBytes, Unpacked: build.MaxUnpackedBytes, Files: build.MaxSourceFiles}
	}
	if opts.ImportAttempts <= 0 {
		opts.ImportAttempts = 6
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{repo: repo, provider: provider, builder: builder, deployer: deployer, projects: projects,
		engine: engine, tx: tx, events: events, opts: opts}
}

var (
	_ source.Commands  = (*Service)(nil)
	_ source.Queries   = (*Service)(nil)
	_ source.Workflows = (*Service)(nil)
)

// refuse is the 422 of a source failure code, in its plain-Spanish words.
func refuse(code string) *errx.Error {
	return errx.Business(source.Message(code)).WithCode(code)
}

// noAccess is GITHUB_NO_ACCESS, a 404 like the provider's own answer when
// the installation does not reach the repository (suspended, or removed).
func noAccess() *errx.Error {
	return errx.NotFound(source.Message(source.CodeNoAccess)).WithCode(source.CodeNoAccess)
}

func (s *Service) enabled() error {
	if !s.provider.Enabled() {
		return refuse(source.CodeNotConfigured)
	}
	return nil
}

// ---- installations ----

func (s *Service) InstallURL(ctx context.Context, organization identity.OrganizationID, user identity.UserID, returnTo string) (string, error) {
	if err := s.enabled(); err != nil {
		return "", err
	}
	if err := validReturn(returnTo); err != nil {
		return "", err
	}
	if len(s.opts.StateKey) == 0 {
		return "", errx.Internal("the install state key is not configured")
	}
	state, err := s.signState(installState{Organization: organization, User: user, Return: returnTo,
		Expires: s.opts.Now().Add(stateLife).Unix()})
	if err != nil {
		return "", err
	}
	return s.provider.InstallPage(state), nil
}

// Setup is the public setup callback: it writes nothing. It checks the state,
// asks the provider to confirm that the GitHub user (by the OAuth code) owns
// the installation's account, and returns a short-lived claim bound to the
// state's organization and user. Only that user, logged in, can redeem it
// with Claim, so a victim who finishes an install link someone else made
// links nothing.
func (s *Service) Setup(ctx context.Context, state string, input source.Link) (source.SetupResult, error) {
	if err := s.enabled(); err != nil {
		return source.SetupResult{}, err
	}
	st, err := s.openState(state)
	if err != nil {
		return source.SetupResult{}, err
	}
	if err := input.Validate(); err != nil {
		return source.SetupResult{}, err
	}
	// The installation id in the URL proves nothing: the user's own OAuth
	// token must show they own or administer the installation's account.
	info, err := s.provider.Installation(ctx, input.ExternalID)
	if err != nil {
		return source.SetupResult{}, err
	}
	if err := s.provider.ConfirmInstaller(ctx, input.Code, info); err != nil {
		return source.SetupResult{}, err
	}
	claim, err := s.signClaim(installClaim{Organization: st.Organization, User: st.User, External: info.ID,
		Return: st.Return, Expires: s.opts.Now().Add(claimLife).Unix()})
	if err != nil {
		return source.SetupResult{}, err
	}
	back := st.Return
	if back == "" {
		back = source.SettingsPage
	}
	return source.SetupResult{Claim: claim, Return: back}, nil
}

// Claim links the installation of a claim to the organization. The claim
// must have been issued to this very organization and user (either mismatch
// is the same STATE_INVALID, so nothing leaks about whose claim it was).
func (s *Service) Claim(ctx context.Context, organization identity.OrganizationID, user identity.UserID, input source.ClaimInstallation) (source.ClaimResult, error) {
	if err := s.enabled(); err != nil {
		return source.ClaimResult{}, err
	}
	if err := input.Validate(); err != nil {
		return source.ClaimResult{}, err
	}
	claim, err := s.openClaim(input.Claim)
	if err != nil {
		return source.ClaimResult{}, err
	}
	if claim.Organization != organization || claim.User != user {
		return source.ClaimResult{}, refuse(source.CodeStateInvalid)
	}
	info, err := s.provider.Installation(ctx, claim.External)
	if err != nil {
		return source.ClaimResult{}, err
	}
	var linked source.Installation
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		linked, err = s.repo.UpsertInstallation(ctx, source.Installation{ID: source.NewInstallationID(),
			Organization: organization, ExternalID: info.ID, Account: info.Account, AccountType: info.AccountType,
			AllRepositories: info.AllRepositories, Suspended: info.Suspended, LinkedBy: user})
		if err != nil {
			return err
		}
		return s.events.Publish(ctx, source.NewInstallationLinked(linked, user))
	})
	if err != nil {
		return source.ClaimResult{}, err
	}
	back := claim.Return
	if back == "" {
		back = source.SettingsPage
	}
	return source.ClaimResult{Installation: linked, Return: back}, nil
}

// Unlink forgets the installation here (its connections stop publishing);
// the App stays installed on GitHub until someone uninstalls it there.
func (s *Service) Unlink(ctx context.Context, organization identity.OrganizationID, id source.InstallationID, input source.Unlink) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		inst, err := s.repo.FindInstallation(ctx, organization, id)
		if err != nil {
			return err
		}
		return s.removeInstallation(ctx, inst, input.UnlinkedBy, source.ReasonUnlinked)
	})
}

// removeInstallation deletes an installation and its connections and
// publishes what stopped, in the caller's transaction.
func (s *Service) removeInstallation(ctx context.Context, inst source.Installation, actor identity.UserID, reason string) error {
	gone, err := s.repo.DeleteConnectionsOfInstallation(ctx, inst.Organization, inst.ID, nil)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteInstallation(ctx, inst.Organization, inst.ID); err != nil {
		return err
	}
	evs := make([]event.Event, 0, len(gone)+1)
	ids := make([]source.ConnectionID, 0, len(gone))
	for _, c := range gone {
		ids = append(ids, c.ID)
		evs = append(evs, source.NewDisconnected(c, actor, reason))
	}
	evs = append(evs, source.NewInstallationUnlinked(inst, actor, ids))
	return s.events.Publish(ctx, evs...)
}

// ---- connections ----

func (s *Service) Connect(ctx context.Context, organization identity.OrganizationID, id project.ID, input source.Connect) (source.Connection, error) {
	if err := s.enabled(); err != nil {
		return source.Connection{}, err
	}
	input = input.Normalize()
	if err := input.Validate(); err != nil {
		return source.Connection{}, err
	}
	p, err := s.projects.Find(ctx, organization, id)
	if err != nil {
		return source.Connection{}, err
	}
	if p.Archived() {
		return source.Connection{}, project.ArchivedError()
	}
	inst, err := s.repo.FindInstallation(ctx, organization, input.Installation)
	if err != nil {
		return source.Connection{}, err
	}
	if inst.Suspended {
		return source.Connection{}, noAccess()
	}
	repo, err := s.provider.Repository(ctx, inst.ExternalID, input.RepositoryID)
	if err != nil {
		return source.Connection{}, err
	}
	branch := input.Branch
	if branch == "" {
		branch = repo.DefaultBranch
	}
	head, _, _, err := s.provider.Head(ctx, inst.ExternalID, repo.ID, branch, "")
	if err != nil {
		return source.Connection{}, err
	}

	var conn source.Connection
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		held, err := s.projects.Hold(ctx, organization, id)
		if err != nil {
			return err
		}
		if held.Archived() {
			return project.ArchivedError()
		}
		if _, err := s.repo.UpsertConnection(ctx, source.Connection{ID: source.NewConnectionID(), Organization: organization,
			Project: id, Installation: inst.ID, RepositoryID: repo.ID, Repository: repo.FullName, Branch: branch,
			Root: input.Root, Config: input.Config, ConnectedBy: input.ConnectedBy}); err != nil {
			return err
		}
		locked, err := s.repo.LockConnection(ctx, organization, id)
		if err != nil {
			return err
		}
		push, err := s.queue(ctx, locked, head, source.TriggerManual, input.ConnectedBy, requester(ctx, input.ConnectedBy), false)
		if err != nil {
			return err
		}
		if conn, err = s.repo.FindConnection(ctx, organization, id); err != nil {
			return err
		}
		return s.events.Publish(ctx, source.NewConnected(conn, input.ConnectedBy), source.NewPushReceived(push))
	})
	return conn, err
}

func (s *Service) Disconnect(ctx context.Context, organization identity.OrganizationID, id project.ID, input source.Disconnect) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.LockConnection(ctx, organization, id)
		if err != nil {
			return err
		}
		if err := s.repo.DeleteConnection(ctx, organization, id); err != nil {
			return err
		}
		return s.events.Publish(ctx, source.NewDisconnected(c, input.DisconnectedBy, source.ReasonUser))
	})
}

// Publish queues the branch's head as it is now, even when it is the last
// commit published (someone asks to publish it again).
func (s *Service) Publish(ctx context.Context, organization identity.OrganizationID, id project.ID, input source.Publish) (source.Push, error) {
	if err := s.enabled(); err != nil {
		return source.Push{}, err
	}
	if err := input.Validate(); err != nil {
		return source.Push{}, err
	}
	c, err := s.repo.FindConnection(ctx, organization, id)
	if err != nil {
		return source.Push{}, err
	}
	p, err := s.projects.Find(ctx, organization, id)
	if err != nil {
		return source.Push{}, err
	}
	if p.Archived() {
		return source.Push{}, project.ArchivedError()
	}
	inst, err := s.repo.FindInstallation(ctx, organization, c.Installation)
	if err != nil {
		return source.Push{}, err
	}
	if inst.Suspended {
		return source.Push{}, noAccess()
	}
	head, _, _, err := s.provider.Head(ctx, inst.ExternalID, c.RepositoryID, c.Branch, "")
	if err != nil {
		return source.Push{}, err
	}
	var push source.Push
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		locked, err := s.repo.LockConnection(ctx, organization, id)
		if err != nil {
			return err
		}
		if locked.ID != c.ID || locked.RepositoryID != c.RepositoryID || locked.Branch != c.Branch {
			return errx.Conflict("the repository connection changed meanwhile; try again")
		}
		if push, err = s.queue(ctx, locked, head, source.TriggerManual, input.RequestedBy, requester(ctx, input.RequestedBy), input.Rebuild); err != nil {
			return err
		}
		return s.events.Publish(ctx, source.NewPushReceived(push))
	})
	return push, err
}

// queue records the commit to publish on the locked connection (making it
// the last commit), drops the older pushes still waiting (only the newest
// commit matters) and starts its import, in the caller's transaction. The
// caller publishes NewPushReceived.
func (s *Service) queue(ctx context.Context, c source.Connection, head source.Commit, trigger source.Trigger, by identity.UserID, who actor.Actor, rebuild bool) (source.Push, error) {
	push := source.Push{ID: source.NewPushID(), Organization: c.Organization, Project: c.Project, Connection: c.ID,
		Git: build.GitRef{Provider: build.ProviderGitHub, Repository: c.Repository, Branch: c.Branch, Commit: head.SHA,
			Title: head.Title, Author: head.Author}.Normalize(),
		Trigger: trigger, Status: source.StatusQueued, RequestedBy: by, Rebuild: rebuild}
	if !by.IsZero() { // a webhook or the poll has no requester, so no client
		push.RequestedClient = event.MetaFrom(ctx).Client
	}
	if err := push.Git.Validate(); err != nil {
		return source.Push{}, errx.Wrap(err, "the provider described the commit badly", errx.TypeExternal)
	}
	if err := s.repo.CreatePush(ctx, push); err != nil {
		return source.Push{}, err
	}
	if _, err := s.repo.SkipPushes(ctx, c.Organization, c.ID, push.ID); err != nil {
		return source.Push{}, err
	}
	// The skipped pushes' runs end on their own: their steps find them finished.
	if _, err := s.engine.Start(ctx, workflow.Start{Workflow: source.WorkflowImport, Organization: c.Organization,
		Actor: by, Subject: event.Subject(source.PushSubjectKind, push.ID), Input: source.ImportInput{Push: push.ID, Actor: who},
		IdempotencyKey: push.ID.String()}); err != nil {
		return source.Push{}, err
	}
	return push, nil
}

// ---- queries ----

func (s *Service) Info(context.Context) source.Info {
	if !s.provider.Enabled() {
		return source.Info{}
	}
	return source.Info{Enabled: true, Slug: s.provider.Slug()}
}

func (s *Service) ListInstallations(ctx context.Context, organization identity.OrganizationID) ([]source.Installation, error) {
	return s.repo.ListInstallations(ctx, organization)
}

// ListRepositories asks the provider for what each of the organization's
// installations reaches (an installation gone on the provider is left out),
// then searches and pages in memory: an installation reaches at most 1000.
func (s *Service) ListRepositories(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[source.Repo], error) {
	page = page.Normalize()
	if err := s.enabled(); err != nil {
		return query.Paginated[source.Repo]{}, err
	}
	insts, err := s.repo.ListInstallations(ctx, organization)
	if err != nil {
		return query.Paginated[source.Repo]{}, err
	}
	search := strings.ToLower(page.Search)
	var all []source.Repo
	for _, inst := range insts {
		if inst.Suspended {
			continue
		}
		repos, err := s.provider.Repositories(ctx, inst.ExternalID)
		if errx.IsType(err, errx.TypeNotFound) {
			continue
		}
		if err != nil {
			return query.Paginated[source.Repo]{}, err
		}
		for _, r := range repos {
			if search == "" || strings.Contains(strings.ToLower(r.FullName), search) {
				r.Installation = inst.ID
				all = append(all, r)
			}
		}
	}
	slices.SortFunc(all, func(a, b source.Repo) int {
		return strings.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName))
	})
	total := len(all)
	lo := min(page.Offset, total)
	hi := min(lo+page.Limit, total)
	return query.NewPaginated(all[lo:hi], total, page), nil
}

func (s *Service) ListBranches(ctx context.Context, organization identity.OrganizationID, installation source.InstallationID, repository int64) ([]source.Branch, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	inst, err := s.repo.FindInstallation(ctx, organization, installation)
	if err != nil {
		return nil, err
	}
	if inst.Suspended {
		return nil, noAccess()
	}
	return s.provider.Branches(ctx, inst.ExternalID, repository)
}

func (s *Service) DetectServices(ctx context.Context, organization identity.OrganizationID, installation source.InstallationID, repository int64, branch, root string) (project.Detected, error) {
	in := project.DetectServices{Root: root}.Normalize()
	if err := in.Validate(); err != nil {
		return project.Detected{}, err
	}
	branch = strings.TrimSpace(branch)
	if err := source.ValidBranch(branch); err != nil {
		return project.Detected{}, err
	}
	if err := s.enabled(); err != nil {
		return project.Detected{}, err
	}
	inst, err := s.repo.FindInstallation(ctx, organization, installation)
	if err != nil {
		return project.Detected{}, err
	}
	if inst.Suspended {
		return project.Detected{}, noAccess()
	}
	if branch == "" {
		repo, err := s.provider.Repository(ctx, inst.ExternalID, repository)
		if err != nil {
			return project.Detected{}, err
		}
		branch = repo.DefaultBranch
	}
	paths, truncated, err := s.provider.Tree(ctx, inst.ExternalID, repository, branch)
	if err != nil {
		return project.Detected{}, err
	}
	in.Paths = paths
	out := project.Detect(in)
	if truncated {
		out.Truncated = true
		out.Notes = append(out.Notes, project.TruncatedNote)
	}
	return out, nil
}

func (s *Service) FindConnection(ctx context.Context, organization identity.OrganizationID, id project.ID) (source.Connection, error) {
	return s.repo.FindConnection(ctx, organization, id)
}

func (s *Service) ListPushes(ctx context.Context, organization identity.OrganizationID, id project.ID, filter source.PushFilter, page query.Pagination) (query.Paginated[source.Push], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[source.Push]{}, err
	}
	if _, err := s.projects.Find(ctx, organization, id); err != nil {
		return query.Paginated[source.Push]{}, err
	}
	return s.repo.ListPushes(ctx, organization, id, filter, page.Normalize())
}

// ---- import workflow ----

// errSkipped ends a run whose push no longer needs publishing (a newer
// commit superseded it, or the repository was disconnected).
var errSkipped = errors.New("the push was superseded")

type imported struct {
	Source build.SourceID `json:"source_id"`
}

// Workflows returns source.import:
//
//	import  download the commit, repack the app's folder, upload it as a build source
//	deploy  start the production deployment of that source (once)
//
// A failure (a refusal, retries exhausted) marks the push failed with the
// reason in plain Spanish.
func (s *Service) Workflows() []workflow.Definition {
	return []workflow.Definition{{
		Name: source.WorkflowImport,
		Steps: []workflow.Step{
			{Name: source.StepImport, Run: s.importCommit, MaxAttempts: s.opts.ImportAttempts, Timeout: 20 * time.Minute},
			{Name: source.StepDeploy, Run: s.deploy, MaxAttempts: deployAttempts},
		},
		OnFailure: s.importFailed,
	}}
}

// current locks the run's connection and push and checks the push still
// matters: unfinished and its connection still the one it was queued on. A
// push that stopped mattering is marked skipped and the run ends.
//
// Locks go connection → push, like Connect and Publish (which lock the
// connection, then skip its older pushes): holding the connection also keeps
// a Disconnect from committing between this check and the deployment.
func (s *Service) current(ctx context.Context, st *workflow.State) (source.Push, source.Connection, error) {
	var in source.ImportInput
	if err := st.Input(&in); err != nil {
		return source.Push{}, source.Connection{}, workflow.Fail(err)
	}
	org := st.Run.Organization
	peek, err := s.repo.FindPush(ctx, org, in.Push)
	if err != nil {
		return source.Push{}, source.Connection{}, err
	}
	c, cerr := s.repo.LockConnection(ctx, org, peek.Project)
	if cerr != nil && !errx.IsType(cerr, errx.TypeNotFound) {
		return source.Push{}, source.Connection{}, cerr
	}
	push, err := s.repo.LockPush(ctx, org, in.Push)
	if err != nil {
		return source.Push{}, source.Connection{}, err
	}
	if push.Status.Finished() {
		return push, source.Connection{}, nil
	}
	if cerr != nil || c.ID != push.Connection {
		push.Status = source.StatusSkipped
		return push, source.Connection{}, s.repo.UpdatePush(ctx, push)
	}
	return push, c, nil
}

func (s *Service) importCommit(ctx context.Context, st *workflow.State) (any, error) {
	var (
		push source.Push
		conn source.Connection
	)
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		if push, conn, err = s.current(ctx, st); err != nil || push.Status.Finished() {
			return err
		}
		if push.Status == source.StatusQueued {
			push.Status = source.StatusImporting
			return s.repo.UpdatePush(ctx, push)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if push.Status.Finished() {
		return nil, workflow.Fail(errSkipped)
	}
	inst, err := s.repo.FindInstallation(ctx, push.Organization, conn.Installation)
	if err != nil {
		return nil, retryable(err) // unlinked meanwhile: NotFound, final
	}
	if inst.Suspended {
		return nil, workflow.Fail(noAccess())
	}
	src, err := s.upload(ctx, inst, conn, push)
	if err != nil {
		return nil, retryable(err)
	}
	return imported{Source: src.ID}, nil
}

// upload streams the provider's tarball through the repack into the build
// domain's upload: nothing is held in memory or on disk.
func (s *Service) upload(ctx context.Context, inst source.Installation, conn source.Connection, push source.Push) (build.Source, error) {
	tarball, err := s.provider.Tarball(ctx, inst.ExternalID, conn.RepositoryID, push.Git.Commit)
	if err != nil {
		return build.Source{}, err
	}
	defer tarball.Close()
	pr, pw := io.Pipe()
	repacked := make(chan error, 1)
	go func() {
		_, err := repack(tarball, pw, conn.Root, s.opts.Limits)
		_ = pw.CloseWithError(err)
		repacked <- err
	}()
	git := push.Git
	src, err := s.builder.Upload(ctx, push.Organization, push.Project,
		build.Upload{Name: conn.Repository, UploadedBy: push.RequestedBy, Git: &git}, pr)
	_ = pr.CloseWithError(errUploadEnded) // stops the repack if the upload gave up early
	if rerr := <-repacked; rerr != nil && !errors.Is(rerr, errUploadEnded) {
		return build.Source{}, rerr // the cause, rather than the upload's view of it
	}
	return src, err
}

// errUploadEnded is what the repack sees when the upload stopped reading.
var errUploadEnded = errors.New("the upload ended")

func (s *Service) deploy(ctx context.Context, st *workflow.State) (any, error) {
	var out imported
	if err := st.Output(source.StepImport, &out); err != nil {
		return nil, workflow.Fail(err)
	}
	var push source.Push
	var in source.ImportInput
	if err := st.Input(&in); err != nil {
		return nil, workflow.Fail(err)
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var (
			conn source.Connection
			err  error
		)
		if push, conn, err = s.current(ctx, st); err != nil || push.Status.Finished() {
			return err
		}
		env, err := s.production(ctx, push)
		if err != nil {
			return err
		}
		// An import queued before 3b carries no actor: judged as the system
		// (no permissions), never as nobody (which would skip IT's rules).
		who := in.Actor
		if who.IsZero() {
			who = github("")
		}
		d, err := s.deployer.DeployUploaded(actor.With(ctx, who), push.Organization, push.Project, env.ID,
			s.release(out.Source, conn, push))
		if err != nil {
			return err
		}
		push.Status, push.Deployment = source.StatusDeployed, &d.ID
		if err := s.repo.UpdatePush(ctx, push); err != nil {
			return err
		}
		return s.events.Publish(ctx, source.NewPushDeployed(push))
	})
	switch {
	case err != nil && refused(err):
		s.discard(ctx, st, out.Source)
		return nil, workflow.Fail(errx.Wrap(err, source.Message(source.CodeDeployRefused), errx.TypeBusiness).
			WithCode(source.CodeDeployRefused))
	case err != nil:
		if st.Attempt >= deployAttempts {
			// the last attempt: the run fails, nothing will build the source
			s.discard(ctx, st, out.Source)
		}
		return nil, err
	case push.Status == source.StatusDeployed:
		return nil, nil // now, or by an earlier attempt whose outcome was lost
	}
	// superseded meanwhile: nothing will build the source
	s.discard(ctx, st, out.Source)
	return nil, workflow.Fail(errSkipped)
}

// deployAttempts bounds the deploy step (a conflict or a database blip is
// retried; release's refusals are not).
const deployAttempts = 8

// discard drops an imported source nothing will build now, rather than at
// the orphan sweep (which still reclaims it if this fails).
func (s *Service) discard(ctx context.Context, st *workflow.State, id build.SourceID) {
	if err := s.builder.Discard(context.WithoutCancel(ctx), st.Run.Organization, id); err != nil {
		slog.Warn("could not discard an imported source", "source", id, "error", err)
	}
}

func (s *Service) production(ctx context.Context, push source.Push) (project.Environment, error) {
	envs, err := s.projects.ListEnvironments(ctx, push.Organization, push.Project,
		project.EnvironmentFilter{Kind: []project.EnvironmentKind{project.KindProduction}})
	if err != nil {
		return project.Environment{}, err
	}
	if len(envs) == 0 {
		return project.Environment{}, errx.Business("the app has no production environment")
	}
	return envs[0], nil
}

// importFailed records why the push was not published (in the transaction
// that fails the run).
func (s *Service) importFailed(ctx context.Context, st *workflow.State, cause error) error {
	if errors.Is(cause, errSkipped) {
		return nil
	}
	var in source.ImportInput
	if err := st.Input(&in); err != nil {
		return nil
	}
	push, err := s.repo.LockPush(ctx, st.Run.Organization, in.Push)
	if err != nil || push.Status.Finished() {
		return err
	}
	f := failureOf(cause)
	if f.Code == source.CodeInternal || f.Code == source.CodeProviderDown {
		// the push only says what happened; the cause stays in the logs
		slog.Warn("a push could not be published", "push", push.ID, "code", f.Code, "error", cause)
	}
	push.Status, push.Error = source.StatusFailed, &f
	if err := s.repo.UpdatePush(ctx, push); err != nil {
		return err
	}
	return s.events.Publish(ctx, source.NewPushFailed(push))
}
