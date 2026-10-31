package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfleet"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
)

// fakeBuilder is the agent's builds in memory (agent/builds against a
// cluster): what tunnel starts, cancels, reports and follows.
type fakeBuilder struct {
	mu        sync.Mutex
	builds    map[string]*fleetv1.Build
	status    map[string]*fleetv1.BuildStatus
	cancelled map[string]bool
	refuse    map[string]bool
	// logs[id] is the build's log; release[id] lets FollowLogs end.
	logs    map[string][]*fleetv1.LogLine
	release map[string]chan struct{}
	notify  func(id string)
}

func newFakeBuilder() *fakeBuilder {
	return &fakeBuilder{builds: map[string]*fleetv1.Build{}, status: map[string]*fleetv1.BuildStatus{}, cancelled: map[string]bool{},
		refuse: map[string]bool{}, logs: map[string][]*fleetv1.LogLine{}, release: map[string]chan struct{}{}}
}

func (f *fakeBuilder) Start(_ context.Context, b *fleetv1.Build) (*fleetv1.BuildStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := b.GetBuildId()
	if f.refuse[id] {
		return &fleetv1.BuildStatus{BuildId: id, Phase: "failed", Reason: "failed", Message: "refused by the agent"}, nil
	}
	if f.builds[id] == nil {
		f.builds[id] = b
		f.status[id] = &fleetv1.BuildStatus{BuildId: id, Phase: "pending"}
		f.release[id] = make(chan struct{})
	}
	return nil, nil
}

func (f *fakeBuilder) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled[id] = true
	delete(f.status, id)
	return nil
}

func (f *fakeBuilder) Status(_ context.Context, id string) (*fleetv1.BuildStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[id], nil
}

func (f *fakeBuilder) Statuses(context.Context) ([]*fleetv1.BuildStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*fleetv1.BuildStatus{}
	for _, s := range f.status {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeBuilder) FollowLogs(ctx context.Context, id string, send func([]*fleetv1.LogLine) error) error {
	f.mu.Lock()
	release := f.release[id]
	f.mu.Unlock()
	if release == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
	}
	f.mu.Lock()
	lines := f.logs[id]
	f.mu.Unlock()
	return send(lines)
}

// set is the build controller: the build is now in st.
func (f *fakeBuilder) set(st *fleetv1.BuildStatus) {
	f.mu.Lock()
	f.status[st.GetBuildId()] = st
	notify := f.notify
	f.mu.Unlock()
	notify(st.GetBuildId())
}

func (f *fakeBuilder) logged(id string, lines ...*fleetv1.LogLine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[id] = lines
	close(f.release[id])
}

func (f *fakeBuilder) started(id string) *fleetv1.Build {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds[id]
}

func (f *fakeBuilder) wasCancelled(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled[id]
}

// TestFleetAgentBuilds is phase 1b-2b's agent side over the real tunnel: the
// agent starts a build the runner sends, fetches its source through its own
// tunnel, sends the log before the final status (so the control plane
// archives the whole log), reports a build it refuses as failed, and cancels.
func TestFleetAgentBuilds(t *testing.T) {
	h := startFleet(t)
	ctx := context.Background()
	f := h.platform.Fleet
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	runner := buildfleet.New(f.Builds)

	src := bytes.Repeat([]byte("odyssey agent source "), 30000)
	sum := sha256.Sum256(src)
	source := build.Source{Organization: org, Size: int64(len(src)), SHA256: hex.EncodeToString(sum[:]),
		Key: blob.TenantKey(org, "sources", uuid.NewString()+".tar.gz")}
	if err := h.platform.Blobs.Put(ctx, source.Key, bytes.NewReader(src), blob.PutOptions{ContentType: "application/gzip"}); err != nil {
		t.Fatal(err)
	}
	newJob := func() build.Job {
		return build.Job{Build: build.Build{ID: build.NewID(), Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}, Source: source}
	}

	created, err := f.Commands.Create(ctx, org, fleet.Create{Name: "lima-1", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: created.Token.Gateway, CAPin: created.Token.CAPin})
	builder := newFakeBuilder()
	cluster := &builderCluster{fakeCluster: newFakeCluster()}
	c := h.tunnelClient(store, cluster.fakeCluster, func(o *tunnel.Options) {
		o.Desired = cluster
		o.Builds = builder
	})
	builder.notify = c.BuildChanged
	runAgent(t, c)
	eventually(t, "the builder to be announced", func() bool {
		cl, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && cl.Connected && cl.Agent.Capabilities.Builder != nil
	})

	// a build: started once, its source fetched through the agent's tunnel
	job := newJob()
	id := job.Build.ID.String()
	if err := runner.Dispatch(ctx, job); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to start the build", func() bool { return builder.started(id) != nil })
	if got := builder.started(id); got.GetSource().GetSha256() != source.SHA256 || !strings.HasPrefix(got.GetImageRepository(), "zot.test:5000/") {
		t.Fatalf("started %+v", got)
	}
	r, err := c.FetchSource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(fetched, src) {
		t.Fatalf("fetched %d bytes (%v), uploaded %d", len(fetched), err, len(src))
	}
	if _, err := c.FetchSource(ctx, uuid.NewString()); err != tunnel.ErrSourceNotFound {
		t.Fatalf("fetch an unknown build: %v", err)
	}

	builder.set(&fleetv1.BuildStatus{BuildId: id, Phase: "running"})
	eventually(t, "the build to run", func() bool {
		o, _ := runner.Observe(ctx, job.Build)
		return o.Phase == build.PhaseRunning
	})

	// it succeeds before its log is out: the status waits for the log
	digest := "sha256:" + strings.Repeat("e", 64)
	builder.set(&fleetv1.BuildStatus{BuildId: id, Phase: "succeeded", ImageDigest: digest})
	time.Sleep(300 * time.Millisecond)
	if o, _ := runner.Observe(ctx, job.Build); o.Phase != build.PhaseRunning {
		t.Fatalf("the final status went before the log: %+v", o)
	}
	builder.logged(id, &fleetv1.LogLine{Seq: 1<<32 | 1, Stream: "prepare", Text: "Fetching the source"},
		&fleetv1.LogLine{Seq: 2<<32 | 1, Stream: "build", Text: "Pushed"})
	eventually(t, "the build to succeed with its whole log", func() bool {
		o, _ := runner.Observe(ctx, job.Build)
		l, _ := runner.Logs(ctx, job.Build, 0, 100)
		return o.Phase == build.PhaseSucceeded && len(l) == 2 && l[1].Text == "Pushed"
	})
	if l, _ := runner.Logs(ctx, job.Build, 0, 100); l[0].Text != "Fetching the source" {
		t.Fatalf("log %+v", l)
	}

	// a build the agent refuses is reported failed
	refused := newJob()
	builder.mu.Lock()
	builder.refuse[refused.Build.ID.String()] = true
	builder.mu.Unlock()
	if err := runner.Dispatch(ctx, refused); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the refused build to fail", func() bool {
		o, _ := runner.Observe(ctx, refused.Build)
		return o.Phase == build.PhaseFailed && strings.Contains(o.Message, "refused by the agent")
	})

	// cancelling reaches the cluster
	if err := runner.Cancel(ctx, job.Build); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent to cancel the build", func() bool { return builder.wasCancelled(id) })
}

// builderCluster announces a builder.
type builderCluster struct{ *fakeCluster }

func (b *builderCluster) Capabilities(ctx context.Context) *fleetv1.Capabilities {
	c := b.fakeCluster.Capabilities(ctx)
	c.Builder = &fleetv1.Builder{Registry: "zot.test:5000", Platforms: []string{"linux/amd64"}}
	return c
}
