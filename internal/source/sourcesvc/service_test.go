package sourcesvc_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/actor"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcefake"
	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcepg"
	"github.com/Abraxas-365/odyssey/internal/source/sourcesvc"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const (
	sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	installExt = 7
	repoWeb    = 42
)

// ---- doubles ----

// engine records Starts (idempotent by workflow and key) so the tests drive
// the steps by hand.
type engine struct {
	mu     sync.Mutex
	runs   map[string]workflow.Run
	starts []workflow.Start
}

func (e *engine) Start(_ context.Context, s workflow.Start) (workflow.RunID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, s)
	key := s.Workflow + "/" + s.IdempotencyKey
	if r, ok := e.runs[key]; ok {
		return r.ID, nil
	}
	in, _ := json.Marshal(s.Input)
	r := workflow.Run{ID: workflow.NewRunID(), Organization: s.Organization, Workflow: s.Workflow, Input: in,
		Outputs: map[string]json.RawMessage{}}
	e.runs[key] = r
	return r.ID, nil
}

func (e *engine) Signal(context.Context, identity.OrganizationID, workflow.RunID, string, any) error {
	return nil
}
func (e *engine) Cancel(context.Context, identity.OrganizationID, workflow.RunID, string) error {
	return nil
}
func (e *engine) Find(context.Context, identity.OrganizationID, workflow.RunID) (workflow.Run, error) {
	return workflow.Run{}, errx.NotFound("run not found")
}

func (e *engine) run(t *testing.T, key string) workflow.Run {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[source.WorkflowImport+"/"+key]
	if !ok {
		t.Fatalf("no %s run for %s", source.WorkflowImport, key)
	}
	return r
}

func (e *engine) has(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.runs[source.WorkflowImport+"/"+key]
	return ok
}

func (e *engine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.runs)
}

// projects is a project.Queries over a few projects, each with a
// production environment; one can be archived.
type projects struct {
	mu   sync.Mutex
	list map[project.ID]*project.Project
	envs map[project.ID]project.Environment
}

func (q *projects) add(org identity.OrganizationID, name string) project.ID {
	q.mu.Lock()
	defer q.mu.Unlock()
	p := &project.Project{ID: project.NewID(), Organization: org, Name: name, Status: project.StatusActive}
	q.list[p.ID] = p
	q.envs[p.ID] = project.Environment{ID: project.NewEnvironmentID(), Organization: org, Project: p.ID,
		Slug: "production", Kind: project.KindProduction}
	return p.ID
}

func (q *projects) archive(id project.ID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.list[id].Status = project.StatusArchived
}

func (q *projects) Find(_ context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.list[id]
	if !ok || p.Organization != org {
		return project.Project{}, errx.NotFound("project not found")
	}
	return *p, nil
}
func (q *projects) FindBySlug(_ context.Context, org identity.OrganizationID, slug string) (project.Project, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.list {
		if p.Organization == org && p.Slug == slug {
			return *p, nil
		}
	}
	return project.Project{}, errx.NotFound("project not found")
}
func (q *projects) Hold(ctx context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	return q.Find(ctx, org, id)
}
func (q *projects) FindEnvironment(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Environment, error) {
	if _, err := q.Find(ctx, org, id); err != nil {
		return project.Environment{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.envs[id].ID != env {
		return project.Environment{}, errx.NotFound("environment not found")
	}
	return q.envs[id], nil
}
func (q *projects) FindEnvironmentBySlug(ctx context.Context, org identity.OrganizationID, id project.ID, _ string) (project.Environment, error) {
	if _, err := q.Find(ctx, org, id); err != nil {
		return project.Environment{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.envs[id], nil
}
func (q *projects) ListEnvironments(ctx context.Context, org identity.OrganizationID, id project.ID, _ project.EnvironmentFilter) ([]project.Environment, error) {
	e, err := q.FindEnvironmentBySlug(ctx, org, id, "production")
	if err != nil {
		return nil, err
	}
	return []project.Environment{e}, nil
}
func (q *projects) List(context.Context, identity.OrganizationID, project.Filter, query.Pagination) (query.Paginated[project.Project], error) {
	return query.Paginated[project.Project]{}, nil
}
func (q *projects) FindMany(context.Context, identity.OrganizationID, []project.ID) ([]project.Project, error) {
	return nil, nil
}
func (q *projects) EnvironmentsOf(context.Context, identity.OrganizationID, []project.ID) ([]project.Environment, error) {
	return nil, nil
}

// upload is what the builder double received.
type upload struct {
	Org     identity.OrganizationID
	Project project.ID
	Input   build.Upload
	Source  build.Source
	Entries []string          // every entry, in order
	Files   map[string]string // regular files
}

// builder reads (and verifies, like the real upload) the whole archive.
type builder struct {
	mu        sync.Mutex
	uploads   []upload
	discarded []build.SourceID
}

func (b *builder) Upload(_ context.Context, org identity.OrganizationID, id project.ID, input build.Upload, archive io.Reader) (build.Source, error) {
	v := srcarchive.NewVerifier(archive, srcarchive.Platform())
	defer v.Close()
	raw, err := io.ReadAll(v)
	if err != nil {
		return build.Source{}, err
	}
	u := upload{Org: org, Project: id, Input: input, Files: map[string]string{}}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return build.Source{}, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return build.Source{}, err
		}
		u.Entries = append(u.Entries, h.Name)
		if h.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(tr)
			if err != nil {
				return build.Source{}, err
			}
			u.Files[strings.TrimPrefix(h.Name, "./")] = string(body)
		}
	}
	u.Source = build.Source{ID: build.NewSourceID(), Organization: org, Project: id, Name: input.Name, Git: input.Git}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.uploads = append(b.uploads, u)
	return u.Source, nil
}

func (b *builder) Discard(_ context.Context, _ identity.OrganizationID, id build.SourceID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.discarded = append(b.discarded, id)
	return nil
}

func (b *builder) all() []upload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.uploads)
}

func (b *builder) gone() []build.SourceID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.discarded)
}

type deployCall struct {
	Org     identity.OrganizationID
	Project project.ID
	Env     project.EnvironmentID
	Input   release.DeployUploaded
	Result  release.Deployment
	Actor   actor.Actor // the actor on the context of the call
}

type deployer struct {
	mu    sync.Mutex
	calls []deployCall
	err   error
}

func (d *deployer) DeployUploaded(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, in release.DeployUploaded) (release.Deployment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return release.Deployment{}, d.err
	}
	dep := release.Deployment{ID: release.NewDeploymentID(), Organization: org, Project: id, Environment: env}
	d.calls = append(d.calls, deployCall{org, id, env, in, dep, actor.From(ctx)})
	return dep, nil
}

func (d *deployer) all() []deployCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

// ---- harness ----

type env struct {
	db       *sqlx.DB
	svc      *sourcesvc.Service
	repo     *sourcepg.Repository
	provider *sourcefake.Fake // nil with a disabled provider
	builder  *builder
	deployer *deployer
	engine   *engine
	events   *eventtest.Recorder
	projects *projects
	defs     map[string]workflow.Definition
	org      identity.OrganizationID
	user     identity.UserID
	offset   time.Duration // added to the clock
}

func setup(t *testing.T) *env { return newEnv(t, false) }

func newEnv(t *testing.T, disabled bool) *env {
	t.Helper()
	db := pgtest.DB(t, migrate.Set{Owner: sourcepg.Schema, Files: sourcepg.Migrations()})
	e := &env{db: db, repo: sourcepg.New(db), builder: &builder{}, deployer: &deployer{},
		engine: &engine{runs: map[string]workflow.Run{}}, events: &eventtest.Recorder{},
		projects: &projects{list: map[project.ID]*project.Project{}, envs: map[project.ID]project.Environment{}},
		org:      identity.NewOrganizationID(), user: identity.MustParseUserID(uuid.NewString())}
	var provider source.GitProvider = sourcefake.Disabled()
	if !disabled {
		e.provider = sourcefake.New(sourcefake.Options{})
		provider = e.provider
		e.provider.AddInstallation(source.InstallationInfo{ID: installExt, Account: "acme", AccountType: "Organization"}, "code-1")
		e.provider.AddRepo(installExt, source.Repo{ID: repoWeb, FullName: "acme/web", DefaultBranch: "main"},
			map[string]source.Commit{
				"main": {SHA: sha1, Title: "first\n\nbody", Author: "ana"},
				"dev":  {SHA: sha2, Title: "second", Author: "luis"},
			})
		e.provider.SetFiles(repoWeb, sha1, map[string]string{
			"package.json":     `{"name":"web"}`,
			"index.js":         "console.log('hi')\n",
			"src/app.js":       "// app\n",
			"apps/web/main.js": "// web\n",
		})
	}
	e.svc = sourcesvc.New(e.repo, provider, e.builder, e.deployer, e.projects, e.engine, txn.Postgres{DB: db}, e.events,
		sourcesvc.Options{StateKey: []byte("0123456789abcdef0123456789abcdef"), ImportAttempts: 3, ConsoleURL: "https://console.test",
			Now: func() time.Time { return time.Now().Add(e.offset) }})
	e.defs = map[string]workflow.Definition{}
	for _, d := range e.svc.Workflows() {
		e.defs[d.Name] = d
	}
	return e
}

func (e *env) project(t *testing.T) project.ID {
	t.Helper()
	return e.projects.add(e.org, "App "+uuid.NewString()[:4])
}

// stateOf asks for an install link and returns its state.
func (e *env) stateOf(t *testing.T, org identity.OrganizationID, returnTo string) string {
	t.Helper()
	raw, err := e.svc.InstallURL(context.Background(), org, e.user, returnTo)
	if err != nil {
		t.Fatalf("InstallURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// link links the fake's installation ext to the organization: the setup
// callback, then the claim by the user who asked.
func (e *env) link(t *testing.T, ext int64, code string) source.Installation {
	t.Helper()
	return e.linkTo(t, e.org, ext, code)
}

func (e *env) linkTo(t *testing.T, org identity.OrganizationID, ext int64, code string) source.Installation {
	t.Helper()
	out, err := e.svc.Setup(context.Background(), e.stateOf(t, org, ""), source.Link{ExternalID: ext, Code: code})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	claimed, err := e.svc.Claim(context.Background(), org, e.user, source.ClaimInstallation{Claim: out.Claim})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return claimed.Installation
}

func cfg() release.Config {
	return release.Config{Port: 3000, HealthPath: "/health", Replicas: 2, Env: map[string]string{"A": "b"}}
}

// connect links the installation and connects a new project to acme/web.
func (e *env) connect(t *testing.T, inst source.Installation, in source.Connect) (project.ID, source.Connection) {
	t.Helper()
	id := e.project(t)
	return id, e.connectProject(t, id, inst, in)
}

func (e *env) connectProject(t *testing.T, id project.ID, inst source.Installation, in source.Connect) source.Connection {
	t.Helper()
	in.Installation, in.RepositoryID, in.ConnectedBy = inst.ID, repoWeb, e.user
	if in.Port == 0 {
		in.Config = cfg()
	}
	c, err := e.svc.Connect(context.Background(), e.org, id, in)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func (e *env) pushes(t *testing.T, id project.ID, status ...source.PushStatus) []source.Push {
	t.Helper()
	out, err := e.svc.ListPushes(context.Background(), e.org, id, source.PushFilter{Status: status}, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func (e *env) onlyPush(t *testing.T, id project.ID) source.Push {
	t.Helper()
	ps := e.pushes(t, id)
	if len(ps) != 1 {
		t.Fatalf("want 1 push, got %d: %+v", len(ps), ps)
	}
	return ps[0]
}

func (e *env) find(t *testing.T, p source.Push) source.Push {
	t.Helper()
	got, err := e.repo.FindPush(context.Background(), p.Organization, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// step runs one step of the push's run, saving its output like the engine does.
func (e *env) step(t *testing.T, p source.Push, name string) error {
	t.Helper()
	key := p.ID.String()
	run := e.engine.run(t, key)
	for i, s := range e.defs[source.WorkflowImport].Steps {
		if s.Name != name {
			continue
		}
		run.Step, run.StepName = i, name
		out, err := s.Run(context.Background(), &workflow.State{Run: run, Attempt: 1})
		if err == nil && out != nil {
			raw, _ := json.Marshal(out)
			e.engine.mu.Lock()
			e.engine.runs[source.WorkflowImport+"/"+key].Outputs[name] = raw
			e.engine.mu.Unlock()
		}
		return err
	}
	t.Fatalf("no step %s", name)
	return nil
}

// onFailure runs the workflow's OnFailure in a transaction, as the engine would.
func (e *env) onFailure(t *testing.T, p source.Push, cause error) {
	t.Helper()
	run := e.engine.run(t, p.ID.String())
	err := txn.Postgres{DB: e.db}.Do(context.Background(), func(ctx context.Context) error {
		return e.defs[source.WorkflowImport].OnFailure(ctx, &workflow.State{Run: run}, cause)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// failedWith runs a step that must fail the run (workflow.Fail) and returns the cause.
func (e *env) failedWith(t *testing.T, p source.Push, name string) error {
	t.Helper()
	err := e.step(t, p, name)
	c, ok := workflow.AsControl(err)
	if !ok || c.Kind != workflow.ControlFail {
		t.Fatalf("step %s: want workflow.Fail, got %v", name, err)
	}
	return err
}

func code(err error) string {
	var x *errx.Error
	if errors.As(err, &x) {
		return x.Code
	}
	return ""
}

func wantCode(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || code(err) != want {
		t.Fatalf("%s: want code %s, got %v", what, want, err)
	}
}

func wantType(t *testing.T, what string, err error, want errx.Type) {
	t.Helper()
	if !errx.IsType(err, want) {
		t.Fatalf("%s: want %s, got %v", what, want, err)
	}
}

func (e *env) wantEvents(t *testing.T, want ...string) {
	t.Helper()
	if got := e.events.Types(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func (e *env) eventsOf(typ string) []event.Event {
	var out []event.Event
	for _, ev := range e.events.Events() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ---- installations ----

func TestInstallURL(t *testing.T) {
	ctx := context.Background()
	t.Run("disabled", func(t *testing.T) {
		e := newEnv(t, true)
		_, err := e.svc.InstallURL(ctx, e.org, e.user, "")
		wantCode(t, "disabled provider", err, source.CodeNotConfigured)
		_, err = e.svc.Setup(ctx, "x.y", source.Link{ExternalID: 1, Code: "c"})
		wantCode(t, "disabled setup", err, source.CodeNotConfigured)
		if _, err := e.svc.Connect(ctx, e.org, e.project(t), source.Connect{Installation: source.NewInstallationID(), RepositoryID: 1,
			Config: cfg(), ConnectedBy: e.user}); code(err) != source.CodeNotConfigured {
			t.Fatalf("disabled connect: %v", err)
		}
		if info := e.svc.Info(ctx); info.Enabled || info.Slug != "" {
			t.Fatalf("info = %+v", info)
		}
	})
	t.Run("bad return_to", func(t *testing.T) {
		e := setup(t)
		for _, bad := range []string{"https://evil.example/x", "//evil.example", "apps/x", "/a b", "/a\\b", "/x://y", "/" + strings.Repeat("a", 250)} {
			_, err := e.svc.InstallURL(ctx, e.org, e.user, bad)
			wantType(t, "return_to "+bad, err, errx.TypeValidation)
		}
	})
	t.Run("url carries a state", func(t *testing.T) {
		e := setup(t)
		raw, err := e.svc.InstallURL(ctx, e.org, e.user, "/apps/x")
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(raw)
		if err != nil || !strings.HasPrefix(raw, "https://github.fake/apps/odyssey-fake/installations/new") {
			t.Fatalf("url = %q (%v)", raw, err)
		}
		state := u.Query().Get("state")
		if payload, sig, ok := strings.Cut(state, "."); !ok || payload == "" || sig == "" {
			t.Fatalf("state = %q", state)
		}
		if other := e.stateOf(t, e.org, "/apps/x"); other == state {
			t.Fatal("two links share a state (no nonce)")
		}
		if info := e.svc.Info(ctx); !info.Enabled || info.Slug != "odyssey-fake" {
			t.Fatalf("info = %+v", info)
		}
		// no state key, no link
		noKey := sourcesvc.New(e.repo, e.provider, e.builder, e.deployer, e.projects, e.engine, txn.Postgres{DB: e.db}, e.events, sourcesvc.Options{})
		_, err = noKey.InstallURL(ctx, e.org, e.user, "")
		wantType(t, "no state key", err, errx.TypeInternal)
	})
}

func TestSetup(t *testing.T) {
	ctx := context.Background()

	t.Run("valid state and code issue a claim and link nothing", func(t *testing.T) {
		e := setup(t)
		state := e.stateOf(t, e.org, "/apps/x")
		out, err := e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		if out.Return != "/apps/x" || out.Claim == "" {
			t.Errorf("result = %+v", out)
		}
		if list, _ := e.svc.ListInstallations(ctx, e.org); len(list) != 0 {
			t.Fatalf("Setup linked %+v", list)
		}
		e.wantEvents(t)

		// the claim links it, for the user who asked
		claimed, err := e.svc.Claim(ctx, e.org, e.user, source.ClaimInstallation{Claim: out.Claim})
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Return != "/apps/x" {
			t.Errorf("return = %q", claimed.Return)
		}
		i := claimed.Installation
		if i.Organization != e.org || i.ExternalID != installExt || i.Account != "acme" || i.AccountType != "Organization" || i.LinkedBy != e.user {
			t.Errorf("installation = %+v", i)
		}
		e.wantEvents(t, source.EventInstallationLinked)
		linked, _ := event.Decode[source.InstallationLinked](e.events.Events()[0])
		if linked.Installation != i.ID || linked.ExternalID != installExt || linked.Account != "acme" {
			t.Errorf("payload = %+v", linked)
		}
		list, err := e.svc.ListInstallations(ctx, e.org)
		if err != nil || len(list) != 1 || list[0].ID != i.ID {
			t.Fatalf("list = %+v %v", list, err)
		}
		if other, _ := e.svc.ListInstallations(ctx, identity.NewOrganizationID()); len(other) != 0 {
			t.Fatalf("another organization sees %+v", other)
		}

		// a second pass refreshes the same installation (idempotent)
		e.events.Reset()
		again, err := e.svc.Setup(ctx, e.stateOf(t, e.org, ""), source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		if again.Return != "/administracion/github" {
			t.Errorf("default return = %q", again.Return)
		}
		reclaimed, err := e.svc.Claim(ctx, e.org, e.user, source.ClaimInstallation{Claim: again.Claim})
		if err != nil || reclaimed.Installation.ID != i.ID || reclaimed.Return != "/administracion/github" {
			t.Fatalf("refresh = %+v %v", reclaimed, err)
		}
		e.wantEvents(t, source.EventInstallationLinked)
		if list, _ := e.svc.ListInstallations(ctx, e.org); len(list) != 1 {
			t.Fatalf("relink duplicated: %+v", list)
		}
	})

	t.Run("forged or expired state", func(t *testing.T) {
		e := setup(t)
		good := e.stateOf(t, e.org, "")
		payload, sig, _ := strings.Cut(good, ".")
		otherState := e.stateOf(t, identity.NewOrganizationID(), "") // someone else's signature must not verify here
		_, otherSig, _ := strings.Cut(otherState, ".")
		for name, bad := range map[string]string{
			"empty":         "",
			"no dot":        "abc",
			"garbage":       "x.y",
			"flipped sig":   payload + "." + flip(sig),
			"swapped sig":   payload + "." + otherSig,
			"flipped body":  flip(payload) + "." + sig,
			"unsigned body": payload + ".",
			"too long":      strings.Repeat("a", 3000) + "." + sig,
		} {
			_, err := e.svc.Setup(ctx, bad, source.Link{ExternalID: installExt, Code: "code-1"})
			if code(err) != source.CodeStateInvalid {
				t.Errorf("%s: want %s, got %v", name, source.CodeStateInvalid, err)
			}
		}
		// a state signed with another key
		foreign := sourcesvc.New(e.repo, e.provider, e.builder, e.deployer, e.projects, e.engine, txn.Postgres{DB: e.db}, e.events,
			sourcesvc.Options{StateKey: []byte("another-key-another-key-another-k")})
		raw, _ := foreign.InstallURL(ctx, e.org, e.user, "")
		u, _ := url.Parse(raw)
		_, err := e.svc.Setup(ctx, u.Query().Get("state"), source.Link{ExternalID: installExt, Code: "code-1"})
		wantCode(t, "foreign key", err, source.CodeStateInvalid)

		// expired
		e.offset = 2 * time.Hour
		_, err = e.svc.Setup(ctx, good, source.Link{ExternalID: installExt, Code: "code-1"})
		wantCode(t, "expired", err, source.CodeStateInvalid)
		e.offset = 0
		if _, err := e.svc.Setup(ctx, good, source.Link{ExternalID: installExt, Code: "code-1"}); err != nil {
			t.Fatalf("the same state before it expires: %v", err)
		}
		e.wantEvents(t) // Setup and its refusals publish nothing
	})

	t.Run("a code whose user does not administer the installation", func(t *testing.T) {
		e := setup(t)
		e.provider.AddInstallation(source.InstallationInfo{ID: 8, Account: "other", AccountType: "User"}, "code-2")
		state := e.stateOf(t, e.org, "")
		// unknown code
		_, err := e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "nope"})
		wantCode(t, "unknown code", err, source.CodeNotYours)
		// a valid code of a user who owns installation 8, not 7 (a plain member of acme, say)
		_, err = e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "code-2"})
		wantCode(t, "other installation", err, source.CodeNotYours)
		wantType(t, "other installation", err, errx.TypeAuthorization)
		// missing id or code
		_, err = e.svc.Setup(ctx, state, source.Link{Code: "code-1"})
		wantType(t, "no installation id", err, errx.TypeValidation)
		_, err = e.svc.Setup(ctx, state, source.Link{ExternalID: installExt})
		wantType(t, "no code", err, errx.TypeValidation)
		// the provider refuses the installer
		e.provider.Fail("ConfirmInstaller", errx.Unauthorized("not the owner").WithCode(source.CodeNotYours))
		_, err = e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "code-1"})
		wantCode(t, "provider refusal", err, source.CodeNotYours)
		if list, _ := e.svc.ListInstallations(ctx, e.org); len(list) != 0 {
			t.Fatalf("linked despite refusals: %+v", list)
		}
		e.wantEvents(t)
	})

	t.Run("another organization has the installation", func(t *testing.T) {
		e := setup(t)
		e.link(t, installExt, "code-1")
		e.events.Reset()
		rival := identity.NewOrganizationID()
		out, err := e.svc.Setup(ctx, e.stateOf(t, rival, ""), source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.Claim(ctx, rival, e.user, source.ClaimInstallation{Claim: out.Claim})
		wantCode(t, "taken", err, source.CodeTaken)
		wantType(t, "taken", err, errx.TypeConflict)
		if list, _ := e.svc.ListInstallations(ctx, rival); len(list) != 0 {
			t.Fatalf("rival got %+v", list)
		}
		e.wantEvents(t)
	})

	t.Run("provider failures are not swallowed", func(t *testing.T) {
		e := setup(t)
		e.provider.Fail("Installation", errx.External("github down"))
		_, err := e.svc.Setup(ctx, e.stateOf(t, e.org, ""), source.Link{ExternalID: installExt, Code: "code-1"})
		wantType(t, "provider down", err, errx.TypeExternal)
		e.provider.Fail("Installation", nil)
		e.provider.Fail("ConfirmInstaller", errx.External("github down"))
		_, err = e.svc.Setup(ctx, e.stateOf(t, e.org, ""), source.Link{ExternalID: installExt, Code: "code-1"})
		wantType(t, "provider down on confirm", err, errx.TypeExternal)
	})
}

func TestClaim(t *testing.T) {
	ctx := context.Background()
	claimOf := func(t *testing.T, e *env, org identity.OrganizationID) string {
		t.Helper()
		out, err := e.svc.Setup(ctx, e.stateOf(t, org, ""), source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		return out.Claim
	}
	input := func(claim string) source.ClaimInstallation { return source.ClaimInstallation{Claim: claim} }

	t.Run("only the user the state was issued to, in its organization", func(t *testing.T) {
		e := setup(t)
		claim := claimOf(t, e, e.org)
		other := identity.MustParseUserID(uuid.NewString())
		_, err := e.svc.Claim(ctx, e.org, other, input(claim))
		wantCode(t, "another user", err, source.CodeStateInvalid)
		wantType(t, "another user", err, errx.TypeBusiness)
		_, err = e.svc.Claim(ctx, identity.NewOrganizationID(), e.user, input(claim))
		wantCode(t, "another organization", err, source.CodeStateInvalid)
		if list, _ := e.svc.ListInstallations(ctx, e.org); len(list) != 0 {
			t.Fatalf("linked despite refusals: %+v", list)
		}
		e.wantEvents(t)
		// the right user still can
		if _, err := e.svc.Claim(ctx, e.org, e.user, input(claim)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a victim finishing the attacker's link only yields a claim the attacker's user cannot get", func(t *testing.T) {
		// The attacker (e.user, org A) sends the install URL to a victim, who
		// installs on their own account: the claim names the attacker's user,
		// and nobody but the attacker's session can redeem it. A victim's own
		// session (another user, even in the same organization) cannot.
		e := setup(t)
		state := e.stateOf(t, e.org, "")
		first, err := e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		replay, err := e.svc.Setup(ctx, state, source.Link{ExternalID: installExt, Code: "code-1"}) // replays are fine: still bound
		if err != nil {
			t.Fatal(err)
		}
		victim := identity.MustParseUserID(uuid.NewString())
		for _, c := range []string{first.Claim, replay.Claim} {
			_, err = e.svc.Claim(ctx, e.org, victim, input(c))
			wantCode(t, "other user", err, source.CodeStateInvalid)
		}
		e.wantEvents(t)
	})

	t.Run("forged, tampered, expired or mixed up claims", func(t *testing.T) {
		e := setup(t)
		claim := claimOf(t, e, e.org)
		payload, sig, _ := strings.Cut(claim, ".")
		for name, bad := range map[string]string{
			"empty":        "",
			"garbage":      "x.y",
			"flipped sig":  payload + "." + flip(sig),
			"flipped body": flip(payload) + "." + sig,
			"no sig":       payload + ".",
			"a state":      e.stateOf(t, e.org, ""), // signed with the same key, another domain
			"too long":     strings.Repeat("a", 3000) + "." + sig,
		} {
			_, err := e.svc.Claim(ctx, e.org, e.user, input(bad))
			if name == "empty" || name == "too long" {
				wantType(t, name, err, errx.TypeValidation)
				continue
			}
			if code(err) != source.CodeStateInvalid {
				t.Errorf("%s: want %s, got %v", name, source.CodeStateInvalid, err)
			}
		}
		// a claim signed with another key
		foreign := sourcesvc.New(e.repo, e.provider, e.builder, e.deployer, e.projects, e.engine, txn.Postgres{DB: e.db}, e.events,
			sourcesvc.Options{StateKey: []byte("another-key-another-key-another-k")})
		out, err := foreign.Setup(ctx, func() string {
			raw, _ := foreign.InstallURL(ctx, e.org, e.user, "")
			u, _ := url.Parse(raw)
			return u.Query().Get("state")
		}(), source.Link{ExternalID: installExt, Code: "code-1"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.Claim(ctx, e.org, e.user, input(out.Claim))
		wantCode(t, "foreign key", err, source.CodeStateInvalid)

		// expired: claims live minutes, far less than states
		e.offset = 16 * time.Minute
		_, err = e.svc.Claim(ctx, e.org, e.user, input(claim))
		wantCode(t, "expired", err, source.CodeStateInvalid)
		e.offset = 10 * time.Minute
		if _, err := e.svc.Claim(ctx, e.org, e.user, input(claim)); err != nil {
			t.Fatalf("before it expires: %v", err)
		}
		e.wantEvents(t, source.EventInstallationLinked)
	})

	t.Run("relinking to the same organization refreshes, another organization is refused", func(t *testing.T) {
		e := setup(t)
		first := e.link(t, installExt, "code-1")
		again := e.link(t, installExt, "code-1")
		if again.ID != first.ID {
			t.Fatalf("relink = %+v, want %s", again, first.ID)
		}
		rival := identity.NewOrganizationID()
		_, err := e.svc.Claim(ctx, rival, e.user, input(claimOf(t, e, rival)))
		wantCode(t, "taken", err, source.CodeTaken)
		wantType(t, "taken", err, errx.TypeConflict)
	})

	t.Run("the provider is asked again at claim time", func(t *testing.T) {
		e := setup(t)
		claim := claimOf(t, e, e.org)
		e.provider.Fail("Installation", errx.External("github down"))
		_, err := e.svc.Claim(ctx, e.org, e.user, input(claim))
		wantType(t, "provider down", err, errx.TypeExternal)
		e.wantEvents(t)
	})

	t.Run("disabled", func(t *testing.T) {
		e := newEnv(t, true)
		_, err := e.svc.Claim(ctx, e.org, e.user, input("x.y"))
		wantCode(t, "disabled claim", err, source.CodeNotConfigured)
	})
}

// flip changes the first character of a base64url string (the last one may
// carry only padding bits, which the decoder ignores).
func flip(s string) string {
	if s == "" {
		return "A"
	}
	repl := byte('A')
	if s[0] == 'A' {
		repl = 'B'
	}
	return string(repl) + s[1:]
}

// ---- connections ----

func TestConnect(t *testing.T) {
	ctx := context.Background()

	t.Run("queues the head", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		e.events.Reset()
		id, conn := e.connect(t, inst, source.Connect{Root: "/apps/web/"})
		if conn.Repository != "acme/web" || conn.Branch != "main" || conn.RepositoryID != repoWeb ||
			conn.Root != "apps/web" || conn.LastCommit != sha1 || conn.Installation != inst.ID || conn.Project != id {
			t.Fatalf("connection = %+v", conn)
		}
		if conn.Config.Port != 3000 || conn.Config.Replicas != 2 || conn.Config.Env["A"] != "b" {
			t.Errorf("config = %+v", conn.Config)
		}
		p := e.onlyPush(t, id)
		if p.Status != source.StatusQueued || p.Trigger != source.TriggerManual || p.Connection != conn.ID || p.RequestedBy != e.user {
			t.Fatalf("push = %+v", p)
		}
		if p.Git.Provider != "github" || p.Git.Repository != "acme/web" || p.Git.Branch != "main" || p.Git.Commit != sha1 ||
			p.Git.Title != "first" || p.Git.Author != "ana" {
			t.Errorf("git = %+v", p.Git)
		}
		e.wantEvents(t, source.EventConnected, source.EventPushReceived)
		recv, _ := event.Decode[source.PushReceived](e.events.Events()[1])
		if recv.Push != p.ID || recv.Commit != sha1 || recv.Trigger != source.TriggerManual || recv.Branch != "main" {
			t.Errorf("push_received = %+v", recv)
		}
		if !e.engine.has(p.ID.String()) || e.engine.count() != 1 {
			t.Fatalf("the import run was not started once, keyed by the push id (%d runs)", e.engine.count())
		}
		e.engine.mu.Lock()
		start := e.engine.starts[0]
		e.engine.mu.Unlock()
		if start.Workflow != source.WorkflowImport || start.Organization != e.org || start.IdempotencyKey != p.ID.String() {
			t.Errorf("start = %+v", start)
		}
		got, err := e.svc.FindConnection(ctx, e.org, id)
		if err != nil || got.ID != conn.ID {
			t.Fatalf("FindConnection = %+v %v", got, err)
		}
		if _, err := e.svc.FindConnection(ctx, identity.NewOrganizationID(), id); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("another organization reads the connection: %v", err)
		}
	})

	t.Run("default branch", func(t *testing.T) {
		e := setup(t)
		_, conn := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		if conn.Branch != "main" {
			t.Fatalf("branch = %q", conn.Branch)
		}
	})

	t.Run("archived project", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id := e.project(t)
		e.projects.archive(id)
		_, err := e.svc.Connect(ctx, e.org, id, source.Connect{Installation: inst.ID, RepositoryID: repoWeb, Config: cfg(), ConnectedBy: e.user})
		wantCode(t, "archived", err, project.CodeArchived)
		if len(e.pushes(t, id)) != 0 || e.engine.count() != 0 {
			t.Fatal("an archived project got a push")
		}
		_, err = e.svc.FindConnection(ctx, e.org, id)
		wantType(t, "no connection", err, errx.TypeNotFound)
	})

	t.Run("unknown or foreign project and installation", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		in := source.Connect{Installation: inst.ID, RepositoryID: repoWeb, Config: cfg(), ConnectedBy: e.user}
		_, err := e.svc.Connect(ctx, e.org, project.NewID(), in)
		wantType(t, "unknown project", err, errx.TypeNotFound)
		in.Installation = source.NewInstallationID()
		_, err = e.svc.Connect(ctx, e.org, e.project(t), in)
		wantType(t, "unknown installation", err, errx.TypeNotFound)
		// another organization's installation is not found either
		rival := identity.NewOrganizationID()
		other := e.projects.add(rival, "theirs")
		in.Installation = inst.ID
		_, err = e.svc.Connect(ctx, rival, other, in)
		wantType(t, "foreign installation", err, errx.TypeNotFound)
		// invalid input
		in.RepositoryID = 0
		_, err = e.svc.Connect(ctx, e.org, e.project(t), in)
		wantType(t, "no repository", err, errx.TypeValidation)
	})

	t.Run("suspended installation", func(t *testing.T) {
		e := setup(t)
		e.provider.AddInstallation(source.InstallationInfo{ID: 9, Account: "paused", AccountType: "User", Suspended: true}, "code-9")
		inst := e.link(t, 9, "code-9")
		if !inst.Suspended {
			t.Fatalf("installation = %+v", inst)
		}
		id := e.project(t)
		_, err := e.svc.Connect(ctx, e.org, id, source.Connect{Installation: inst.ID, RepositoryID: repoWeb, Config: cfg(), ConnectedBy: e.user})
		wantCode(t, "suspended", err, source.CodeNoAccess)
		if len(e.pushes(t, id)) != 0 {
			t.Fatal("pushes on a suspended installation")
		}
	})

	t.Run("repository not reached", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		e.provider.RemoveRepo(repoWeb)
		_, err := e.svc.Connect(ctx, e.org, e.project(t), source.Connect{Installation: inst.ID, RepositoryID: repoWeb, Config: cfg(), ConnectedBy: e.user})
		wantCode(t, "removed repo", err, source.CodeNoAccess)
	})

	t.Run("missing branch", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id := e.project(t)
		_, err := e.svc.Connect(ctx, e.org, id, source.Connect{Installation: inst.ID, RepositoryID: repoWeb, Branch: "nope", Config: cfg(), ConnectedBy: e.user})
		wantCode(t, "missing branch", err, source.CodeBranchMissing)
		if _, err := e.svc.FindConnection(ctx, e.org, id); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("a connection was left behind: %v", err)
		}
	})

	t.Run("reconnect to another branch", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, first := e.connect(t, inst, source.Connect{})
		if first.LastCommit != sha1 {
			t.Fatalf("first = %+v", first)
		}
		e.events.Reset()
		second := e.connectProject(t, id, inst, source.Connect{Branch: "dev", Root: "src"})
		if second.ID != first.ID {
			t.Errorf("reconnect changed the connection id")
		}
		if second.Branch != "dev" || second.Root != "src" || second.LastCommit != sha2 {
			t.Fatalf("second = %+v (last commit must follow the new branch)", second)
		}
		e.wantEvents(t, source.EventConnected, source.EventPushReceived)
		if all := e.pushes(t, id); len(all) != 2 || all[0].Git.Branch != "dev" || all[0].Git.Commit != sha2 || all[0].Status != source.StatusQueued {
			t.Fatalf("pushes = %+v", all)
		}
		// the push of the old branch no longer matters
		if skipped := e.pushes(t, id, source.StatusSkipped); len(skipped) != 1 || skipped[0].Git.Branch != "main" {
			t.Fatalf("skipped = %+v", skipped)
		}
	})
}

func TestPublish(t *testing.T) {
	ctx := context.Background()

	t.Run("always queues, even for the same commit", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		first := e.onlyPush(t, id)
		e.events.Reset()

		p, err := e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		if err != nil {
			t.Fatal(err)
		}
		if p.ID == first.ID || p.Status != source.StatusQueued || p.Trigger != source.TriggerManual || p.Git.Commit != sha1 || p.RequestedBy != e.user {
			t.Fatalf("push = %+v", p)
		}
		e.wantEvents(t, source.EventPushReceived)
		if !e.engine.has(p.ID.String()) || e.engine.count() != 2 {
			t.Fatalf("runs = %d", e.engine.count())
		}
		if got := e.find(t, first); got.Status != source.StatusSkipped {
			t.Fatalf("the older queued push is %s, want skipped", got.Status)
		}
		if got := e.find(t, p); got.Status != source.StatusQueued {
			t.Fatalf("the new push is %s", got.Status)
		}
		// listing: newest first, filtered by status
		all := e.pushes(t, id)
		if len(all) != 2 || all[0].ID != p.ID || all[1].ID != first.ID {
			t.Fatalf("pushes = %+v", all)
		}
		if q := e.pushes(t, id, source.StatusQueued); len(q) != 1 || q[0].ID != p.ID {
			t.Fatalf("queued = %+v", q)
		}
		_, err = e.svc.ListPushes(ctx, e.org, id, source.PushFilter{Status: []source.PushStatus{"bogus"}}, query.Pagination{})
		wantType(t, "bad status filter", err, errx.TypeValidation)

		// a branch that moved publishes the new head
		e.provider.Push(repoWeb, "main", source.Commit{SHA: sha2, Title: "moved", Author: "ana"})
		moved, err := e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		if err != nil || moved.Git.Commit != sha2 || moved.Git.Title != "moved" {
			t.Fatalf("moved = %+v %v", moved, err)
		}
		if c, _ := e.svc.FindConnection(ctx, e.org, id); c.LastCommit != sha2 {
			t.Errorf("last commit = %s", c.LastCommit)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := setup(t)
		_, err := e.svc.Publish(ctx, e.org, e.project(t), source.Publish{RequestedBy: e.user})
		wantType(t, "not connected", err, errx.TypeNotFound)

		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		e.provider.Fail("Head", errx.External("github down"))
		_, err = e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		wantType(t, "provider down", err, errx.TypeExternal)
		e.provider.Fail("Head", nil)
		if n := len(e.pushes(t, id)); n != 1 {
			t.Fatalf("pushes = %d after a failed publish", n)
		}
		e.projects.archive(id)
		_, err = e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		wantCode(t, "archived", err, project.CodeArchived)
		// another organization cannot publish it
		_, err = e.svc.Publish(ctx, identity.NewOrganizationID(), id, source.Publish{RequestedBy: e.user})
		wantType(t, "foreign org", err, errx.TypeNotFound)
	})

	t.Run("suspended installation", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		e.provider.AddInstallation(source.InstallationInfo{ID: installExt, Account: "acme", AccountType: "Organization", Suspended: true})
		e.link(t, installExt, "code-1") // refreshes: now suspended
		_, err := e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		wantCode(t, "suspended", err, source.CodeNoAccess)
	})
}

// ---- the import workflow ----

func TestImportWorkflowEndToEnd(t *testing.T) {
	e := setup(t)
	inst := e.link(t, installExt, "code-1")
	id, conn := e.connect(t, inst, source.Connect{})
	push := e.onlyPush(t, id)
	e.events.Reset()

	if err := e.step(t, push, source.StepImport); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := e.find(t, push); got.Status != source.StatusImporting {
		t.Fatalf("after import the push is %s", got.Status)
	}
	ups := e.builder.all()
	if len(ups) != 1 {
		t.Fatalf("uploads = %d", len(ups))
	}
	up := ups[0]
	if up.Org != e.org || up.Project != id {
		t.Errorf("uploaded for %v/%v", up.Org, up.Project)
	}
	want := []string{"index.js", "package.json", "src/app.js", "apps/web/main.js"}
	slices.Sort(want)
	if got := sortedKeys(up.Files); !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, name := range up.Entries {
		n := strings.TrimPrefix(name, "./")
		if strings.HasPrefix(n, "acme-web") || strings.HasPrefix(n, ".git") || n == "pax_global_header" {
			t.Errorf("entry %q leaks the repository's wrapper", name)
		}
	}
	if up.Files["package.json"] != `{"name":"web"}` || up.Files["src/app.js"] != "// app\n" {
		t.Errorf("file contents = %v", up.Files)
	}
	g := up.Input.Git
	if g == nil || g.Provider != "github" || g.Repository != "acme/web" || g.Branch != "main" || g.Commit != sha1 || g.Title != "first" || g.Author != "ana" {
		t.Fatalf("git ref = %+v", g)
	}
	if up.Input.Name != "acme/web" || up.Input.UploadedBy != e.user {
		t.Errorf("upload input = %+v", up.Input)
	}

	if err := e.step(t, push, source.StepDeploy); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	calls := e.deployer.all()
	if len(calls) != 1 {
		t.Fatalf("deploys = %d", len(calls))
	}
	c := calls[0]
	prod, _ := e.projects.FindEnvironmentBySlug(context.Background(), e.org, id, "production")
	if c.Org != e.org || c.Project != id || c.Env != prod.ID || c.Input.Source != up.Source.ID || c.Input.RequestedBy != e.user {
		t.Fatalf("deploy call = %+v", c)
	}
	if !reflect.DeepEqual(c.Input.Config, conn.Config.Normalize()) {
		t.Errorf("deploy config = %+v, want %+v", c.Input.Config, conn.Config)
	}
	done := e.find(t, push)
	if done.Status != source.StatusDeployed || done.Deployment == nil || *done.Deployment != c.Result.ID {
		t.Fatalf("push = %+v", done)
	}
	e.wantEvents(t, source.EventPushDeployed)
	dep, _ := event.Decode[source.PushDeployed](e.events.Events()[0])
	if dep.Push != push.ID || dep.Deployment != c.Result.ID.String() || dep.Commit != sha1 {
		t.Errorf("push_deployed = %+v", dep)
	}
	if byDeploy, err := e.repo.FindPushByDeployment(context.Background(), e.org, c.Result.ID); err != nil || byDeploy.ID != push.ID {
		t.Fatalf("FindPushByDeployment = %+v %v", byDeploy, err)
	}

	// the step runs at least once: running it again deploys nothing
	e.events.Reset()
	if err := e.step(t, push, source.StepDeploy); err != nil {
		t.Fatalf("deploy again: %v", err)
	}
	if n := len(e.deployer.all()); n != 1 {
		t.Fatalf("deploys after a re-run = %d", n)
	}
	e.wantEvents(t)
	if got := e.find(t, push); got.Status != source.StatusDeployed || *got.Deployment != c.Result.ID {
		t.Fatalf("re-run changed the push: %+v", got)
	}
	if len(e.builder.gone()) != 0 {
		t.Errorf("the deployed source was discarded")
	}
}

func TestImportRoot(t *testing.T) {
	e := setup(t)
	inst := e.link(t, installExt, "code-1")

	t.Run("only the app's folder, re-rooted", func(t *testing.T) {
		id, _ := e.connect(t, inst, source.Connect{Root: "apps/web"})
		push := e.onlyPush(t, id)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatal(err)
		}
		ups := e.builder.all()
		got := ups[len(ups)-1]
		if keys := sortedKeys(got.Files); !slices.Equal(keys, []string{"main.js"}) {
			t.Fatalf("files = %v", keys)
		}
		if got.Files["main.js"] != "// web\n" {
			t.Errorf("content = %q", got.Files["main.js"])
		}
	})

	t.Run("a folder that is not in the commit fails the push", func(t *testing.T) {
		id, _ := e.connect(t, inst, source.Connect{Root: "nope/app"})
		push := e.onlyPush(t, id)
		before := len(e.builder.all())
		cause := e.failedWith(t, push, source.StepImport)
		if code(cause) != source.CodeRootMissing {
			t.Fatalf("cause = %v", cause)
		}
		if len(e.builder.all()) != before {
			t.Error("a source was uploaded for a missing folder")
		}
		e.events.Reset()
		e.onFailure(t, push, cause)
		got := e.find(t, push)
		if got.Status != source.StatusFailed || got.Error == nil || got.Error.Code != source.CodeRootMissing ||
			got.Error.Message != source.Message(source.CodeRootMissing) || got.Error.Detail == "" {
			t.Fatalf("push = %+v / %+v", got, got.Error)
		}
		e.wantEvents(t, source.EventPushFailed)
		pf, _ := event.Decode[source.PushFailed](e.events.Events()[0])
		if pf.Push != push.ID || pf.Code != source.CodeRootMissing || pf.Commit != sha1 {
			t.Errorf("push_failed = %+v", pf)
		}
		// OnFailure again (the engine retries a rolled-back marking): no second record
		e.events.Reset()
		e.onFailure(t, push, cause)
		e.wantEvents(t)
	})
}

func TestImportSuperseded(t *testing.T) {
	ctx := context.Background()

	t.Run("a newer commit supersedes a queued push", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		old := e.onlyPush(t, id)
		if _, err := e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user}); err != nil {
			t.Fatal(err)
		}
		e.events.Reset()
		cause := e.failedWith(t, old, source.StepImport)
		if !strings.Contains(cause.Error(), "superseded") {
			t.Errorf("cause = %v", cause)
		}
		e.onFailure(t, old, cause)
		got := e.find(t, old)
		if got.Status != source.StatusSkipped || got.Error != nil {
			t.Fatalf("push = %+v", got)
		}
		e.wantEvents(t) // no failure record
		if len(e.builder.all()) != 0 || len(e.deployer.all()) != 0 {
			t.Error("a superseded push imported or deployed")
		}
	})

	t.Run("superseded while importing: the source is dropped, nothing deploys", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		old := e.onlyPush(t, id)
		if err := e.step(t, old, source.StepImport); err != nil {
			t.Fatal(err)
		}
		newer, err := e.svc.Publish(ctx, e.org, id, source.Publish{RequestedBy: e.user})
		if err != nil {
			t.Fatal(err)
		}
		e.events.Reset()
		cause := e.failedWith(t, old, source.StepDeploy)
		e.onFailure(t, old, cause)
		if len(e.deployer.all()) != 0 {
			t.Fatal("a superseded push deployed")
		}
		if gone := e.builder.gone(); len(gone) != 1 || gone[0] != e.builder.all()[0].Source.ID {
			t.Fatalf("discarded = %v", gone)
		}
		if got := e.find(t, old); got.Status != source.StatusSkipped || got.Error != nil {
			t.Fatalf("old = %+v", got)
		}
		e.wantEvents(t)
		// the newer push goes through
		if err := e.step(t, newer, source.StepImport); err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, newer, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		if got := e.find(t, newer); got.Status != source.StatusDeployed {
			t.Fatalf("newer = %+v", got)
		}
		if n := len(e.deployer.all()); n != 1 {
			t.Fatalf("deploys = %d", n)
		}
	})

	t.Run("disconnected before the import", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.svc.Disconnect(ctx, e.org, id, source.Disconnect{DisconnectedBy: e.user}); err != nil {
			t.Fatal(err)
		}
		cause := e.failedWith(t, push, source.StepImport)
		e.onFailure(t, push, cause)
		if got := e.find(t, push); got.Status != source.StatusSkipped || got.Error != nil {
			t.Fatalf("push = %+v", got)
		}
		if len(e.builder.all()) != 0 {
			t.Error("imported after disconnecting")
		}
	})

	t.Run("reconnected: the old connection's push is skipped", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.svc.Disconnect(ctx, e.org, id, source.Disconnect{DisconnectedBy: e.user}); err != nil {
			t.Fatal(err)
		}
		e.connectProject(t, id, inst, source.Connect{}) // a new connection id
		cause := e.failedWith(t, push, source.StepImport)
		e.onFailure(t, push, cause)
		if got := e.find(t, push); got.Status != source.StatusSkipped {
			t.Fatalf("push = %+v", got)
		}
	})
}

func TestImportProviderFailures(t *testing.T) {
	t.Run("a provider that is down is retried, not failed", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		e.provider.Fail("Tarball", errx.External("github is down"))
		err := e.step(t, push, source.StepImport)
		if err == nil {
			t.Fatal("import succeeded with the provider down")
		}
		if _, ok := workflow.AsControl(err); ok {
			t.Fatalf("a transient failure must be retryable, got workflow control: %v", err)
		}
		wantType(t, "transient", err, errx.TypeExternal)
		if got := e.find(t, push); got.Status != source.StatusImporting {
			t.Fatalf("push = %s while retrying", got.Status)
		}
		if len(e.builder.all()) != 0 {
			t.Error("uploaded something")
		}
		// the provider recovers: the retry goes through
		e.provider.Fail("Tarball", nil)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if len(e.builder.all()) != 1 {
			t.Fatalf("uploads = %d", len(e.builder.all()))
		}
		// and when retries run out, the engine records a provider-down failure
		e.onFailure(t, push, errx.External("github is down"))
		got := e.find(t, push)
		if got.Status != source.StatusFailed || got.Error.Code != source.CodeProviderDown {
			t.Fatalf("push = %+v / %+v", got, got.Error)
		}
	})

	t.Run("a repository the installation lost fails at once", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		e.provider.RemoveRepo(repoWeb)
		cause := e.failedWith(t, push, source.StepImport)
		if code(cause) != source.CodeNoAccess {
			t.Fatalf("cause = %v", cause)
		}
		e.onFailure(t, push, cause)
		if got := e.find(t, push); got.Status != source.StatusFailed || got.Error.Code != source.CodeNoAccess {
			t.Fatalf("push = %+v / %+v", got, got.Error)
		}
	})

	t.Run("a suspended installation fails the push", func(t *testing.T) {
		e := setup(t)
		inst := e.link(t, installExt, "code-1")
		id, _ := e.connect(t, inst, source.Connect{})
		push := e.onlyPush(t, id)
		e.provider.AddInstallation(source.InstallationInfo{ID: installExt, Account: "acme", AccountType: "Organization", Suspended: true})
		e.link(t, installExt, "code-1")
		cause := e.failedWith(t, push, source.StepImport)
		e.onFailure(t, push, cause)
		if got := e.find(t, push); got.Status != source.StatusFailed || got.Error.Code != source.CodeNoAccess {
			t.Fatalf("push = %+v / %+v", got, got.Error)
		}
	})

	t.Run("a refused deployment fails the push and drops the source", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatal(err)
		}
		e.deployer.err = errx.Business("the project is archived").WithCode(project.CodeArchived)
		cause := e.failedWith(t, push, source.StepDeploy)
		if code(cause) != source.CodeDeployRefused {
			t.Fatalf("cause = %v", cause)
		}
		if len(e.builder.gone()) != 1 {
			t.Error("the source was not discarded")
		}
		e.onFailure(t, push, cause)
		if got := e.find(t, push); got.Status != source.StatusFailed || got.Error.Code != source.CodeDeployRefused {
			t.Fatalf("push = %+v / %+v", got, got.Error)
		}
	})

	t.Run("a deploy that fails for a transient reason is retried", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatal(err)
		}
		e.deployer.err = errx.Internal("database hiccup")
		err := e.step(t, push, source.StepDeploy)
		if _, ok := workflow.AsControl(err); err == nil || ok {
			t.Fatalf("want a retryable error, got %v", err)
		}
		if got := e.find(t, push); got.Status != source.StatusImporting {
			t.Fatalf("push = %s", got.Status)
		}
		e.deployer.err = nil
		if err := e.step(t, push, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		if got := e.find(t, push); got.Status != source.StatusDeployed {
			t.Fatalf("push = %s", got.Status)
		}
	})
}

// ---- the actor of a deferred deploy, and IT's refusals ----

func TestDeployJudgedAsWhoAsked(t *testing.T) {
	ctx := context.Background()

	t.Run("a manual publish carries the requester and the permissions of their token", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		asked := actor.With(ctx, actor.Actor{User: e.user, Permissions: []string{"deploy", "policy.manage"}})
		p, err := e.svc.Publish(asked, e.org, id, source.Publish{RequestedBy: e.user})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, p, source.StepImport); err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, p, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		calls := e.deployer.all()
		if len(calls) != 1 {
			t.Fatalf("deploys = %d", len(calls))
		}
		want := actor.Actor{User: e.user, Permissions: []string{"deploy", "policy.manage"}}
		if !reflect.DeepEqual(calls[0].Actor, want) {
			t.Fatalf("actor = %+v, want %+v", calls[0].Actor, want)
		}
	})

	t.Run("a requester with no token on the context holds no permission", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		// someone else's token is not the requester's
		other := actor.With(ctx, actor.Actor{User: identity.MustParseUserID(uuid.NewString()), Permissions: []string{"policy.manage"}})
		p, err := e.svc.Publish(other, e.org, id, source.Publish{RequestedBy: e.user})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, p, source.StepImport); err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, p, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		if got := e.deployer.all()[0].Actor; got.User != e.user || len(got.Permissions) != 0 {
			t.Fatalf("actor = %+v", got)
		}
	})

	t.Run("a webhook push is the system actor of its sender, with no permissions", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		if err := e.deliver("d-1", "push", e.pushBody("main", sha2)); err != nil {
			t.Fatal(err)
		}
		p := e.pushes(t, id)[0]
		if p.Trigger != source.TriggerWebhook {
			t.Fatalf("push = %+v", p)
		}
		e.provider.SetFiles(repoWeb, sha2, map[string]string{"package.json": `{"name":"web"}`, "index.js": "x\n"})
		if err := e.step(t, p, source.StepImport); err != nil {
			t.Fatal(err)
		}
		if err := e.step(t, p, source.StepDeploy); err != nil {
			t.Fatal(err)
		}
		got := e.deployer.all()[0].Actor
		if got.System != "github:ana" || !got.User.IsZero() || len(got.Permissions) != 0 || got.ID() != "github:ana" {
			t.Fatalf("actor = %+v", got)
		}
	})
}

func TestDeployRefusedByITIsDefinitive(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *errx.Error
	}{
		{"denied", errx.Forbidden("La regla de TI «Sin viernes» no permite esto.").WithCode(gate.CodeDenied)},
		{"needs approval", errx.Conflict("Esta acción necesita aprobación de TI (regla «Sin viernes»).").WithCode(gate.CodeApprovalRequired)},
	} {
		t.Run(tc.name+": no retries, the rule is in the failure", func(t *testing.T) {
			e := setup(t)
			id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
			push := e.onlyPush(t, id)
			if err := e.step(t, push, source.StepImport); err != nil {
				t.Fatal(err)
			}
			e.deployer.err = tc.err
			cause := e.failedWith(t, push, source.StepDeploy) // workflow.Fail on the first attempt, not a retry
			if len(e.builder.gone()) != 1 {
				t.Error("the source was not discarded")
			}
			e.onFailure(t, push, cause)
			got := e.find(t, push)
			if got.Status != source.StatusFailed || got.Error == nil || got.Error.Code != source.CodeDeployRefused ||
				!strings.Contains(got.Error.Detail, "Sin viernes") {
				t.Fatalf("push = %+v / %+v", got, got.Error)
			}
		})
	}

	t.Run("another conflict is still retried", func(t *testing.T) {
		e := setup(t)
		id, _ := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
		push := e.onlyPush(t, id)
		if err := e.step(t, push, source.StepImport); err != nil {
			t.Fatal(err)
		}
		e.deployer.err = errx.Conflict("serialization failure")
		err := e.step(t, push, source.StepDeploy)
		if _, ok := workflow.AsControl(err); err == nil || ok {
			t.Fatalf("want a retryable error, got %v", err)
		}
	})
}

// ---- disconnect and unlink ----

func TestDisconnect(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	id, conn := e.connect(t, e.link(t, installExt, "code-1"), source.Connect{})
	e.events.Reset()

	if err := e.svc.Disconnect(ctx, e.org, id, source.Disconnect{DisconnectedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	e.wantEvents(t, source.EventDisconnected)
	d, _ := event.Decode[source.Disconnected](e.events.Events()[0])
	if d.Connection != conn.ID || d.Reason != source.ReasonUser || d.Repository != "acme/web" || d.Project != id.String() {
		t.Errorf("disconnected = %+v", d)
	}
	if _, err := e.svc.FindConnection(ctx, e.org, id); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("connection still there: %v", err)
	}
	if n := len(e.pushes(t, id)); n != 1 {
		t.Errorf("history lost: %d pushes", n)
	}
	e.events.Reset()
	err := e.svc.Disconnect(ctx, e.org, id, source.Disconnect{DisconnectedBy: e.user})
	wantType(t, "second disconnect", err, errx.TypeNotFound)
	e.wantEvents(t)
	err = e.svc.Disconnect(ctx, identity.NewOrganizationID(), id, source.Disconnect{DisconnectedBy: e.user})
	wantType(t, "foreign org", err, errx.TypeNotFound)
}

func TestUnlink(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	inst := e.link(t, installExt, "code-1")
	a, connA := e.connect(t, inst, source.Connect{})
	b, connB := e.connect(t, inst, source.Connect{})

	// another organization cannot unlink it
	err := e.svc.Unlink(ctx, identity.NewOrganizationID(), inst.ID, source.Unlink{UnlinkedBy: e.user})
	wantType(t, "foreign org", err, errx.TypeNotFound)

	e.events.Reset()
	if err := e.svc.Unlink(ctx, e.org, inst.ID, source.Unlink{UnlinkedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	e.wantEvents(t, source.EventDisconnected, source.EventDisconnected, source.EventInstallationUnlinked)
	evs := e.events.Events()
	gotConns := map[source.ConnectionID]bool{}
	for _, ev := range evs[:2] {
		d, _ := event.Decode[source.Disconnected](ev)
		if d.Reason != source.ReasonUnlinked {
			t.Errorf("reason = %q", d.Reason)
		}
		gotConns[d.Connection] = true
	}
	if !gotConns[connA.ID] || !gotConns[connB.ID] {
		t.Errorf("disconnected = %v", gotConns)
	}
	u, _ := event.Decode[source.InstallationUnlinked](evs[2])
	if u.Installation != inst.ID || u.Account != "acme" || len(u.Connections) != 2 ||
		!slices.Contains(u.Connections, connA.ID) || !slices.Contains(u.Connections, connB.ID) {
		t.Errorf("installation_unlinked = %+v", u)
	}
	for _, p := range []project.ID{a, b} {
		if _, err := e.svc.FindConnection(ctx, e.org, p); !errx.IsType(err, errx.TypeNotFound) {
			t.Fatalf("connection of %s survived: %v", p, err)
		}
	}
	if list, _ := e.svc.ListInstallations(ctx, e.org); len(list) != 0 {
		t.Fatalf("installations = %+v", list)
	}
	if n := len(e.pushes(t, a)) + len(e.pushes(t, b)); n != 2 {
		t.Errorf("history lost: %d pushes", n)
	}
	e.events.Reset()
	err = e.svc.Unlink(ctx, e.org, inst.ID, source.Unlink{UnlinkedBy: e.user})
	wantType(t, "second unlink", err, errx.TypeNotFound)
	e.wantEvents(t)
	// the installation can be linked again
	e.link(t, installExt, "code-1")
}

// ---- repositories and branches ----

func TestListRepositories(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	// installation 7: acme/web (+ api, Zeta); installation 8: other/db; 9 is suspended with a repo
	e.provider.AddRepo(installExt, source.Repo{ID: 43, FullName: "acme/api", DefaultBranch: "main"}, nil)
	e.provider.AddRepo(installExt, source.Repo{ID: 44, FullName: "acme/Zeta", DefaultBranch: "main", Private: true}, nil)
	e.provider.AddInstallation(source.InstallationInfo{ID: 8, Account: "other", AccountType: "User"}, "code-8")
	e.provider.AddRepo(8, source.Repo{ID: 50, FullName: "other/db-web", DefaultBranch: "trunk"}, nil)
	e.provider.AddInstallation(source.InstallationInfo{ID: 9, Account: "paused", AccountType: "User", Suspended: true}, "code-9")
	e.provider.AddRepo(9, source.Repo{ID: 60, FullName: "paused/hidden", DefaultBranch: "main"}, nil)
	i7 := e.link(t, installExt, "code-1")
	i8 := e.link(t, 8, "code-8")
	e.link(t, 9, "code-9")
	// an installation the provider no longer knows is left out, not an error
	if _, err := e.repo.UpsertInstallation(ctx, source.Installation{ID: source.NewInstallationID(), Organization: e.org,
		ExternalID: 777, Account: "ghost", AccountType: "User", LinkedBy: e.user}); err != nil {
		t.Fatal(err)
	}
	// another organization's installation is never listed
	rival := identity.NewOrganizationID()
	e.provider.AddInstallation(source.InstallationInfo{ID: 10, Account: "rival", AccountType: "User"}, "code-10")
	e.provider.AddRepo(10, source.Repo{ID: 70, FullName: "rival/secret", DefaultBranch: "main"}, nil)
	if got := e.linkTo(t, rival, 10, "code-10"); got.Organization != rival {
		t.Fatalf("rival link = %+v", got)
	}

	names := func(p query.Paginated[source.Repo]) []string {
		var out []string
		for _, r := range p.Items {
			out = append(out, r.FullName)
		}
		return out
	}
	all, err := e.svc.ListRepositories(ctx, e.org, query.Pagination{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(all), []string{"acme/api", "acme/web", "acme/Zeta", "other/db-web"}; !slices.Equal(got, want) {
		t.Fatalf("repositories = %v, want %v", got, want)
	}
	if all.Page.Total != 4 {
		t.Errorf("total = %d", all.Page.Total)
	}
	for _, r := range all.Items {
		want := i7.ID
		if r.FullName == "other/db-web" {
			want = i8.ID
		}
		if r.Installation != want {
			t.Errorf("%s belongs to installation %v, want %v", r.FullName, r.Installation, want)
		}
	}
	if !all.Items[2].Private {
		t.Error("Private lost")
	}

	search, err := e.svc.ListRepositories(ctx, e.org, query.Pagination{Search: "WEB"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(search), []string{"acme/web", "other/db-web"}; !slices.Equal(got, want) || search.Page.Total != 2 {
		t.Fatalf("search = %v (total %d), want %v", got, search.Page.Total, want)
	}

	page2, err := e.svc.ListRepositories(ctx, e.org, query.Pagination{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(page2), []string{"acme/Zeta", "other/db-web"}; !slices.Equal(got, want) || page2.Page.Total != 4 || page2.Page.Offset != 2 || page2.Page.Limit != 2 {
		t.Fatalf("page 2 = %v %+v", got, page2.Page)
	}
	past, err := e.svc.ListRepositories(ctx, e.org, query.Pagination{Limit: 2, Offset: 10})
	if err != nil || len(past.Items) != 0 || past.Page.Total != 4 {
		t.Fatalf("past the end = %+v %v", past, err)
	}
	none, err := e.svc.ListRepositories(ctx, e.org, query.Pagination{Search: "zzz"})
	if err != nil || none.Items == nil || len(none.Items) != 0 {
		t.Fatalf("no match = %+v %v", none, err)
	}
	// a provider outage is an error, not an empty list
	e.provider.Fail("Repositories", errx.External("github down"))
	_, err = e.svc.ListRepositories(ctx, e.org, query.Pagination{})
	wantType(t, "outage", err, errx.TypeExternal)
}

func TestListBranches(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	inst := e.link(t, installExt, "code-1")
	bs, err := e.svc.ListBranches(ctx, e.org, inst.ID, repoWeb)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0].Name != "dev" || bs[0].Commit != sha2 || bs[1].Name != "main" {
		t.Fatalf("branches = %+v", bs)
	}
	_, err = e.svc.ListBranches(ctx, identity.NewOrganizationID(), inst.ID, repoWeb)
	wantType(t, "foreign org", err, errx.TypeNotFound)
	_, err = e.svc.ListBranches(ctx, e.org, inst.ID, 9999)
	wantCode(t, "unknown repo", err, source.CodeNoAccess)
}
