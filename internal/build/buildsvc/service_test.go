package buildsvc_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/blob/adapters/gocloudblob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfake"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildpg"
	"github.com/Abraxas-365/odyssey/internal/build/archive"
	"github.com/Abraxas-365/odyssey/internal/build/buildsvc"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/migrate"
	"github.com/Abraxas-365/odyssey/internal/pgtest"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// engine records Starts (idempotent by workflow and key) so the test drives
// the steps by hand.
type engine struct {
	mu   sync.Mutex
	runs map[string]workflow.Run
}

func (e *engine) Start(_ context.Context, s workflow.Start) (workflow.RunID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
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

func (e *engine) run(t *testing.T, wf string, b build.ID) workflow.Run {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[wf+"/"+b.String()]
	if !ok {
		t.Fatalf("no %s run for %s", wf, b)
	}
	return r
}

func (e *engine) started(wf string, b build.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.runs[wf+"/"+b.String()]
	return ok
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

// projects is a project.Queries with one project and environment.
type projects struct {
	p project.Project
	e project.Environment
}

func (q projects) Find(_ context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	if org != q.p.Organization || id != q.p.ID {
		return project.Project{}, errx.NotFound("project not found")
	}
	return q.p, nil
}
func (q projects) FindEnvironment(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID) (project.Environment, error) {
	if _, err := q.Find(ctx, org, id); err != nil || env != q.e.ID {
		return project.Environment{}, errx.NotFound("environment not found")
	}
	return q.e, nil
}
func (q projects) List(context.Context, identity.OrganizationID, project.Filter, query.Pagination) (query.Paginated[project.Project], error) {
	return query.Paginated[project.Project]{}, nil
}
func (q projects) FindEnvironmentBySlug(context.Context, identity.OrganizationID, project.ID, string) (project.Environment, error) {
	return q.e, nil
}
func (q projects) ListEnvironments(context.Context, identity.OrganizationID, project.ID, project.EnvironmentFilter) ([]project.Environment, error) {
	return []project.Environment{q.e}, nil
}
func (q projects) Hold(ctx context.Context, org identity.OrganizationID, id project.ID) (project.Project, error) {
	return q.Find(ctx, org, id)
}

type env struct {
	db      *sqlx.DB
	svc     *buildsvc.Service
	defs    map[string]workflow.Definition
	runner  *buildfake.Runner
	blobs   blob.Store
	engine  *engine
	events  *eventtest.Recorder
	org     identity.OrganizationID
	user    identity.UserID
	project project.ID
	env     project.EnvironmentID
	outcome buildfake.Outcome
	spy     *spy
}

// spy records where archived logs are read from.
type spy struct {
	blob.Store
	mu      sync.Mutex
	offsets []int64
}

func (s *spy) OpenAt(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	s.mu.Lock()
	s.offsets = append(s.offsets, offset)
	s.mu.Unlock()
	return s.Store.OpenAt(ctx, key, offset)
}

func (s *spy) reads() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.offsets
	s.offsets = nil
	return out
}

func setup(t *testing.T) *env { return setupWith(t, buildfake.Options{}, buildsvc.Options{}) }

// setupWith takes the fake runner's options (its Behavior is the env's
// outcome) and the service's (small archive limits unless set).
func setupWith(t *testing.T, ro buildfake.Options, so buildsvc.Options) *env {
	t.Helper()
	db := pgtest.DB(t, migrate.Set{Owner: buildpg.Schema, Files: buildpg.Migrations()})
	blobs, err := gocloudblob.Open(context.Background(), "mem://")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	org := identity.NewOrganizationID()
	q := projects{p: project.Project{ID: project.NewID(), Organization: org, Name: "Visitas", Status: project.StatusActive},
		e: project.Environment{ID: project.NewEnvironmentID(), Slug: "production"}}
	e := &env{db: db, blobs: blobs, engine: &engine{runs: map[string]workflow.Run{}}, events: &eventtest.Recorder{}, org: org,
		user: identity.MustParseUserID(uuid.NewString()), project: q.p.ID, env: q.e.ID}
	ro.Behavior = func(build.Job) buildfake.Outcome { return e.outcome }
	e.runner = buildfake.New(ro)
	if so.Limits == (archive.Limits{}) {
		so.Limits = archive.Limits{Compressed: 1 << 20, Unpacked: 4 << 20, Files: 100}
	}
	e.spy = &spy{Store: blobs}
	e.svc = buildsvc.New(buildpg.New(db), q, e.runner, e.spy, e.engine, txn.Postgres{DB: db}, e.events, so)
	e.defs = map[string]workflow.Definition{}
	for _, d := range e.svc.Workflows() {
		e.defs[d.Name] = d
	}
	return e
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *env) upload(t *testing.T) build.Source {
	t.Helper()
	s, err := e.svc.Upload(context.Background(), e.org, e.project, build.Upload{Name: "/home/ana/visitas", UploadedBy: e.user},
		bytes.NewReader(tgz(t, map[string]string{"go.mod": "module visitas\n", "main.go": "package main\n"})))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) start(t *testing.T, src build.Source) build.Build {
	t.Helper()
	b, err := e.svc.Start(context.Background(), e.org, build.Start{ID: build.NewID(), Project: e.project, Environment: e.env,
		Source: src.ID, RequestedBy: e.user})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// step runs one step of b's run, saving its output like the engine does.
func (e *env) step(t *testing.T, wf string, b build.ID, name string) error {
	t.Helper()
	run := e.engine.run(t, wf, b)
	for i, s := range e.defs[wf].Steps {
		if s.Name != name {
			continue
		}
		run.Step, run.StepName = i, name
		out, err := s.Run(context.Background(), &workflow.State{Run: run, Attempt: 1})
		if err == nil && out != nil {
			raw, _ := json.Marshal(out)
			e.engine.mu.Lock()
			e.engine.runs[wf+"/"+b.String()].Outputs[name] = raw
			e.engine.mu.Unlock()
		}
		return err
	}
	t.Fatalf("no step %s in %s", name, wf)
	return nil
}

// steps runs build.run's steps in order, failing on any error.
func (e *env) steps(t *testing.T, b build.ID, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := e.step(t, build.WorkflowRun, b, n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

// fail runs b's OnFailure with cause at step, as the engine would.
func (e *env) fail(t *testing.T, b build.ID, step string, cause error) {
	t.Helper()
	run := e.engine.run(t, build.WorkflowRun, b)
	run.StepName = step
	err := txn.Postgres{DB: e.db}.Do(context.Background(), func(ctx context.Context) error {
		return e.defs[build.WorkflowRun].OnFailure(ctx, &workflow.State{Run: run}, cause)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) find(t *testing.T, id build.ID) build.Build {
	t.Helper()
	b, err := e.svc.Find(context.Background(), e.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) count(typ string) int {
	n := 0
	for _, x := range e.events.Types() {
		if x == typ {
			n++
		}
	}
	return n
}

func isPoll(err error) bool {
	var c *workflow.Control
	return errors.As(err, &c) && c.Kind == workflow.ControlPoll
}

func isFail(err error) bool {
	var c *workflow.Control
	return errors.As(err, &c) && c.Kind == workflow.ControlFail
}

func TestUpload(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	s := e.upload(t)
	if s.Name != "visitas" || s.Files != 2 || len(s.SHA256) != 64 || s.Size == 0 ||
		!strings.HasPrefix(s.Key, "org/"+e.org.String()+"/sources/") {
		t.Fatalf("source %+v", s)
	}
	r, err := e.blobs.Open(ctx, s.Key)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(r)
	r.Close()
	if int64(len(stored)) != s.Size {
		t.Fatalf("stored %d bytes, recorded %d", len(stored), s.Size)
	}
	if e.count(build.EventSourceUploaded) != 1 {
		t.Fatalf("events %v", e.events.Types())
	}

	// A bad archive stores nothing and records nothing.
	_, err = e.svc.Upload(ctx, e.org, e.project, build.Upload{Name: "x"}, strings.NewReader("not an archive"))
	var x *errx.Error
	if !errors.As(err, &x) || x.Code != build.CodeSourceInvalid {
		t.Fatalf("bad archive: %v", err)
	}
	big := tgz(t, map[string]string{"big": strings.Repeat("ab", 3<<20)})
	if _, err := e.svc.Upload(ctx, e.org, e.project, build.Upload{Name: "x"}, bytes.NewReader(big)); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("bomb: %v", err)
	}
	if len(e.events.Types()) != 1 {
		t.Fatal("a bad archive published an event")
	}
	if _, err := e.svc.Upload(ctx, identity.NewOrganizationID(), e.project, build.Upload{Name: "x"}, bytes.NewReader(big)); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant's project: %v", err)
	}
}

func TestBuildSucceeds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.upload(t)
	b := e.start(t, src)
	if b.Status != build.StatusQueued || b.Run.IsZero() {
		t.Fatalf("started %+v", b)
	}
	// Start is idempotent by build id; a different source under it is a conflict.
	again, err := e.svc.Start(ctx, e.org, build.Start{ID: b.ID, Project: e.project, Environment: e.env, Source: src.ID})
	if err != nil || again.ID != b.ID || again.Run != b.Run {
		t.Fatalf("again %+v %v", again, err)
	}
	other := e.upload(t)
	if _, err := e.svc.Start(ctx, e.org, build.Start{ID: b.ID, Project: e.project, Environment: e.env, Source: other.ID}); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("reused id: %v", err)
	}
	if _, err := e.svc.Start(ctx, identity.NewOrganizationID(), build.Start{ID: build.NewID(), Project: e.project, Environment: e.env, Source: src.ID}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant's source: %v", err)
	}

	e.steps(t, b.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	got := e.find(t, b.ID)
	if got.Status != build.StatusSucceeded || got.Image != e.runner.Image(got) || got.FinishedAt == nil {
		t.Fatalf("finished %+v", got)
	}
	// Live log before archiving, archived after; the runner forgets it.
	page, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{})
	if err != nil || len(page.Lines) == 0 || page.Finished {
		t.Fatalf("live log %+v %v", page, err)
	}
	if err := e.step(t, build.WorkflowArchive, b.ID, build.StepArchive); err != nil {
		t.Fatal(err)
	}
	if err := e.step(t, build.WorkflowArchive, b.ID, build.StepArchive); err != nil {
		t.Fatalf("archive twice: %v", err)
	}
	if !e.runner.Forgotten(b.ID) {
		t.Fatal("runner did not forget")
	}
	archived, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{})
	if err != nil || !archived.Finished || len(archived.Lines) != len(page.Lines) {
		t.Fatalf("archived log %+v %v", archived, err)
	}
	last := archived.Lines[len(archived.Lines)-1]
	if !strings.Contains(last.Text, got.Image) {
		t.Fatalf("last line %q", last.Text)
	}
	tail, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{After: last.Seq - 1})
	if err != nil || len(tail.Lines) != 1 || tail.Next != last.Seq || !tail.Finished {
		t.Fatalf("tail %+v %v", tail, err)
	}
	if _, err := e.svc.Logs(ctx, identity.NewOrganizationID(), b.ID, build.LogQuery{}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant's log: %v", err)
	}
	want := []string{build.EventSourceUploaded, build.EventStarted, build.EventSourceUploaded, build.EventSucceeded}
	if got := e.events.Types(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v", got)
	}
}

func TestBuildFails(t *testing.T) {
	e := setup(t)
	e.outcome = buildfake.Outcome{Reason: build.ReasonUnsupported, Message: "no start command"}
	b := e.start(t, e.upload(t))
	e.steps(t, b.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	got := e.find(t, b.ID)
	if got.Status != build.StatusFailed || got.Error == nil || got.Error.Code != build.CodeUnsupported ||
		got.Error.Detail != "no start command" || got.Image != "" {
		t.Fatalf("failed %+v %+v", got, got.Error)
	}
	if e.count(build.EventFailed) != 1 || !e.engine.started(build.WorkflowArchive, b.ID) {
		t.Fatalf("events %v", e.events.Types())
	}
}

// A line of only bytes JSON escapes to six ("<", control bytes) still
// archives into a log that can be read back.
func TestArchivedLogSurvivesEscapedLines(t *testing.T) {
	e := setup(t)
	e.outcome = buildfake.Outcome{Reason: build.ReasonFailed, Message: strings.Repeat("<\x01", 8<<10)}
	b := e.start(t, e.upload(t))
	e.steps(t, b.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	if err := e.step(t, build.WorkflowArchive, b.ID, build.StepArchive); err != nil {
		t.Fatal(err)
	}
	page, err := e.svc.Logs(context.Background(), e.org, b.ID, build.LogQuery{})
	if err != nil || !page.Finished || len(page.Lines) == 0 {
		t.Fatalf("archived log %+v %v", page, err)
	}
	last := page.Lines[len(page.Lines)-1].Text
	if !strings.HasPrefix(last, "error: failed <\x01") || !strings.HasSuffix(last, "…") {
		t.Fatalf("last line %q", last[:min(len(last), 40)])
	}
}

// A big archived log is paged from its index, not read from the top each
// time; a mark that does not point at its line falls back to the top.
func TestArchivedLogIsIndexed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.outcome = buildfake.Outcome{Lines: 3 * build.LogIndexEvery}
	b := e.start(t, e.upload(t))
	e.steps(t, b.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	if err := e.step(t, build.WorkflowArchive, b.ID, build.StepArchive); err != nil {
		t.Fatal(err)
	}
	stored := e.find(t, b.ID)
	if len(stored.LogsIndex) != 3 {
		t.Fatalf("index %+v", stored.LogsIndex)
	}
	e.spy.reads()

	var all []build.LogLine
	var afters []int64
	after := int64(0)
	for {
		afters = append(afters, after)
		page, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{After: after, Limit: 700})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Lines...)
		after = page.Next
		if page.Finished {
			break
		}
	}
	total := len(all)
	if total < 3*build.LogIndexEvery || all[total-1].Seq != int64(total) {
		t.Fatalf("read %d lines, last %+v", total, all[total-1])
	}
	for i, l := range all {
		if l.Seq != int64(i+1) {
			t.Fatalf("line %d has seq %d", i, l.Seq)
		}
	}
	reads := e.spy.reads()
	if len(reads) != len(afters) || reads[len(reads)-1] == 0 {
		t.Fatalf("read from %v (index %+v)", reads, stored.LogsIndex)
	}
	for i, a := range afters {
		if want := stored.LogsIndex.Start(a).Offset; reads[i] != want {
			t.Fatalf("page after %d read from %d, want %d", a, reads[i], want)
		}
	}

	// A wrong mark: the page is still right, read from the top.
	if _, err := e.db.ExecContext(ctx, `UPDATE build.builds SET logs_index = $2 WHERE id = $1`, b.ID,
		fmt.Sprintf(`[{"seq": %d, "offset": %d}]`, 2*build.LogIndexEvery, stored.LogsIndex[1].Offset+3)); err != nil {
		t.Fatal(err)
	}
	page, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{After: int64(2 * build.LogIndexEvery), Limit: 2})
	if err != nil || len(page.Lines) != 2 || page.Lines[0].Seq != int64(2*build.LogIndexEvery+1) {
		t.Fatalf("after a wrong mark %+v %v", page, err)
	}
	if reads := e.spy.reads(); len(reads) != 2 || reads[1] != 0 {
		t.Fatalf("wrong mark read from %v", reads)
	}
}

// A full quota drops the oldest built sources early; when none can go, the
// upload is refused before its bytes are read.
func TestSourceQuota(t *testing.T) {
	probe := setup(t)
	size := probe.upload(t).Size
	// Uploads differ by a few bytes (tgz's map order): room for two, not three.
	e := setupWith(t, buildfake.Options{}, buildsvc.Options{SourceQuota: 2*size + size/2})
	ctx := context.Background()
	first, second := e.upload(t), e.upload(t)
	body := func() io.Reader {
		return bytes.NewReader(tgz(t, map[string]string{"go.mod": "module visitas\n", "main.go": "package main\n"}))
	}

	// Full, and nothing was built: refused, nothing stored.
	_, err := e.svc.Upload(ctx, e.org, e.project, build.Upload{Name: "x"}, body())
	var x *errx.Error
	if !errors.As(err, &x) || x.Code != build.CodeSourceQuota || x.HTTPStatus != 422 {
		t.Fatalf("over quota: %v", err)
	}
	// A source being built is not dropped either.
	running := e.start(t, first)
	if _, err := e.svc.Upload(ctx, e.org, e.project, build.Upload{Name: "x"}, body()); !errors.As(err, &x) ||
		x.Code != build.CodeSourceQuota {
		t.Fatalf("over quota while building: %v", err)
	}
	if e.count(build.EventSourceUploaded) != 2 {
		t.Fatalf("a refused upload was recorded: %v", e.events.Types())
	}
	// When even dropping every built source leaves no room, the body is
	// not read at all.
	if _, err := e.db.ExecContext(ctx, `UPDATE build.sources SET size = $2 WHERE id = $1`, second.ID, 2*size); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Upload(ctx, e.org, e.project, build.Upload{Name: "x"}, errReader{}); !errors.As(err, &x) ||
		x.Code != build.CodeSourceQuota {
		t.Fatalf("hopelessly over quota: %v", err)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE build.sources SET size = $2 WHERE id = $1`, second.ID, second.Size); err != nil {
		t.Fatal(err)
	}
	// Once built, the oldest goes to make room.
	e.steps(t, running.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	third := e.upload(t)
	if got, _ := e.svc.FindSource(ctx, e.org, first.ID); !got.Expired() {
		t.Fatal("the oldest built source was kept")
	}
	if ok, _ := e.blobs.Exists(ctx, first.Key); ok {
		t.Fatal("the dropped source's bytes are still stored")
	}
	for _, s := range []build.Source{second, third} {
		if got, _ := e.svc.FindSource(ctx, e.org, s.ID); got.Expired() {
			t.Fatalf("%s expired", s.ID)
		}
	}
	if e.count(build.EventSourceExpired) != 1 {
		t.Fatalf("events %v", e.events.Types())
	}
	if got := e.find(t, running.ID); got.Image == "" {
		t.Fatal("the build lost its image")
	}
	// The sweep has nothing left to delete for it.
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("sweep %d %v", n, err)
	}
}

// errReader fails the test's upload if its body is ever read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the body was read") }

func TestDiscard(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	unused, used := e.upload(t), e.upload(t)
	e.start(t, used)
	if err := e.svc.Discard(ctx, e.org, unused.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.FindSource(ctx, e.org, unused.ID); !got.Expired() {
		t.Fatal("not expired")
	}
	if ok, _ := e.blobs.Exists(ctx, unused.Key); ok {
		t.Fatal("bytes still stored")
	}
	if err := e.svc.Discard(ctx, e.org, used.ID); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("discarded a source a build uses: %v", err)
	}
	if err := e.svc.Discard(ctx, identity.NewOrganizationID(), used.ID); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("cross-tenant discard: %v", err)
	}
	if e.count(build.EventSourceExpired) != 1 {
		t.Fatalf("events %v", e.events.Types())
	}
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("sweep %d %v", n, err)
	}
}

func TestBuildRefusedAndTimedOut(t *testing.T) {
	e := setup(t)
	e.outcome = buildfake.Outcome{Refuse: build.CodeNoBuilder}
	b := e.start(t, e.upload(t))
	err := e.step(t, build.WorkflowRun, b.ID, build.StepDispatch)
	if !isFail(err) {
		t.Fatalf("a refusal should fail the run: %v", err)
	}
	e.fail(t, b.ID, build.StepDispatch, err)
	e.fail(t, b.ID, build.StepDispatch, err) // OnFailure runs again after a crash
	if got := e.find(t, b.ID); got.Status != build.StatusFailed || got.Error.Code != build.CodeNoBuilder {
		t.Fatalf("refused %+v", got.Error)
	}
	// It never ran: its log is only the refusal.
	if page, err := e.svc.Logs(context.Background(), e.org, b.ID, build.LogQuery{}); err != nil || len(page.Lines) != 1 ||
		page.Lines[0].Text != "error: "+build.CodeNoBuilder {
		t.Fatalf("refused log %+v %v", page, err)
	}
	if e.count(build.EventFailed) != 1 || !e.engine.started(build.WorkflowArchive, b.ID) {
		t.Fatalf("events %v", e.events.Types())
	}

	// An unreachable runner is retried, not failed.
	e.outcome = buildfake.Outcome{}
	slow := e.start(t, e.upload(t))
	e.runner.SetDown(errx.External("tunnel down"))
	if err := e.step(t, build.WorkflowRun, slow.ID, build.StepDispatch); err == nil || isFail(err) {
		t.Fatalf("down runner: %v", err)
	}
	e.runner.SetDown(nil)
	// The engine's timeout ends the wait step; OnFailure words it.
	e.fail(t, slow.ID, build.StepWait, errx.Internal("step wait timed out"))
	if got := e.find(t, slow.ID); got.Status != build.StatusFailed || got.Error.Code != build.CodeTimeout {
		t.Fatalf("timeout %+v", got.Error)
	}
}

func TestCancel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	b := e.start(t, e.upload(t))
	e.steps(t, b.ID, build.StepDispatch)
	if err := e.svc.Cancel(ctx, e.org, b.ID, build.Cancel{CancelledBy: e.user}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(ctx, e.org, b.ID, build.Cancel{}); err != nil {
		t.Fatalf("cancel twice: %v", err)
	}
	if err := e.svc.Cancel(ctx, identity.NewOrganizationID(), b.ID, build.Cancel{}); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another tenant's build: %v", err)
	}
	// The run notices: wait and finish change nothing and hand it to the
	// archive, which stops it in the runner.
	e.steps(t, b.ID, build.StepWait, build.StepFinish)
	if got := e.find(t, b.ID); got.Status != build.StatusCancelled || got.Image != "" {
		t.Fatalf("cancelled %+v", got)
	}
	if err := e.step(t, build.WorkflowArchive, b.ID, build.StepArchive); err != nil {
		t.Fatal(err)
	}
	if !e.runner.Cancelled(b.ID) {
		t.Fatal("the runner was not told to stop")
	}
	if e.count(build.EventCancelled) != 1 || e.count(build.EventSucceeded) != 0 {
		t.Fatalf("events %v", e.events.Types())
	}
	page, err := e.svc.Logs(ctx, e.org, b.ID, build.LogQuery{})
	if err != nil || !page.Finished || !strings.Contains(page.Lines[len(page.Lines)-1].Text, "cancelada") {
		t.Fatalf("cancelled log %+v %v", page, err)
	}
}

func TestWaitPollsWhileRunning(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := setupWith(t, buildfake.Options{Duration: time.Minute, Now: clock}, buildsvc.Options{})
	b := e.start(t, e.upload(t))
	mu.Lock()
	now = b.CreatedAt.Add(30 * time.Second)
	mu.Unlock()
	e.steps(t, b.ID, build.StepDispatch)
	if err := e.step(t, build.WorkflowRun, b.ID, build.StepWait); !isPoll(err) {
		t.Fatalf("want a poll, got %v", err)
	}
	if got := e.find(t, b.ID); got.Status != build.StatusRunning || got.StartedAt == nil {
		t.Fatalf("running %+v", got)
	}
	page, err := e.svc.Logs(context.Background(), e.org, b.ID, build.LogQuery{})
	if err != nil || len(page.Lines) < 2 || len(page.Lines) > 6 || page.Finished {
		t.Fatalf("half-way log: %d lines, %v", len(page.Lines), err)
	}
	mu.Lock()
	now = b.CreatedAt.Add(2 * time.Minute)
	mu.Unlock()
	if err := e.step(t, build.WorkflowRun, b.ID, build.StepWait); err != nil {
		t.Fatalf("done: %v", err)
	}
}

func TestSweep(t *testing.T) {
	e := setupWith(t, buildfake.Options{}, buildsvc.Options{Retention: time.Hour, OrphanRetention: time.Minute})
	ctx := context.Background()
	built, busy, orphan, fresh := e.upload(t), e.upload(t), e.upload(t), e.upload(t)
	done := e.start(t, built)
	e.steps(t, done.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	running := e.start(t, busy)
	age := func(s build.Source, d time.Duration) {
		t.Helper()
		if _, err := e.db.ExecContext(ctx, `UPDATE build.sources SET created_at = now() - $2::interval WHERE id = $1`,
			s.ID, d.String()); err != nil {
			t.Fatal(err)
		}
	}
	age(built, 2*time.Hour)
	age(busy, 2*time.Hour)
	age(orphan, 10*time.Minute)
	_ = fresh

	n, err := e.svc.Sweep(ctx)
	if err != nil || n != 2 {
		t.Fatalf("swept %d, %v", n, err)
	}
	for s, gone := range map[build.Source]bool{built: true, orphan: true, busy: false, fresh: false} {
		ok, err := e.blobs.Exists(ctx, s.Key)
		if err != nil || ok == gone {
			t.Errorf("%s: exists=%v want gone=%v (%v)", s.Name, ok, gone, err)
		}
		got, _ := e.svc.FindSource(ctx, e.org, s.ID)
		if got.Expired() != gone {
			t.Errorf("source expired=%v want %v", got.Expired(), gone)
		}
	}
	if e.count(build.EventSourceExpired) != 2 {
		t.Fatalf("events %v", e.events.Types())
	}
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("second sweep %d %v", n, err)
	}
	// An expired source cannot be built; the image built from it stays.
	_, err = e.svc.Start(ctx, e.org, build.Start{ID: build.NewID(), Project: e.project, Environment: e.env, Source: built.ID})
	var x *errx.Error
	if !errors.As(err, &x) || x.Code != build.CodeSourceExpired {
		t.Fatalf("expired source: %v", err)
	}
	if got := e.find(t, done.ID); got.Image == "" {
		t.Fatal("the build lost its image")
	}
	// Once the busy build ends, its source goes in the next sweep.
	e.steps(t, running.ID, build.StepDispatch, build.StepWait, build.StepFinish)
	if n, err := e.svc.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("after the build ended: %d %v", n, err)
	}
}
