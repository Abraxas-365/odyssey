package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/agent/tunnel"
	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/build/adapters/buildfleet"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestFleetBuild is phase 1b-2's protocol, end to end over real gRPC and
// mutual TLS: the build domain's runner sends a build to the environment's
// cluster; its agent (speaking the raw protocol here) receives it, fetches
// the source from the gateway, streams log lines and reports the image;
// the runner observes all of it, and cancelling tells the cluster.
func TestFleetBuild(t *testing.T) {
	h := startFleet(t)
	ctx := context.Background()
	f := h.platform.Fleet
	org, user := identity.NewOrganizationID(), identity.MustParseUserID(uuid.NewString())
	runner := buildfleet.New(f.Builds)

	// the uploaded source, where the API put it
	src := bytes.Repeat([]byte("odyssey source "), 40000) // > one chunk
	sum := sha256.Sum256(src)
	source := build.Source{Organization: org, Size: int64(len(src)), SHA256: hex.EncodeToString(sum[:]),
		Key: blob.TenantKey(org, "sources", uuid.NewString()+".tar.gz")}
	if err := h.platform.Blobs.Put(ctx, source.Key, bytes.NewReader(src), blob.PutOptions{ContentType: "application/gzip"}); err != nil {
		t.Fatal(err)
	}
	b := build.Build{ID: build.NewID(), Organization: org, Project: project.NewID(), Environment: project.NewEnvironmentID()}
	job := build.Job{Build: b, Source: source}

	if err := runner.Dispatch(ctx, job); !isRefused(err, build.CodeNoCluster) {
		t.Fatalf("dispatch without a cluster: %v", err)
	}

	// a cluster whose agent announces no builder refuses builds
	created, err := f.Commands.Create(ctx, org, fleet.Create{Name: "lima-1", CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	store := tunnel.NewMemoryStore(tunnel.Enrollment{Token: created.Token.Value, Gateway: created.Token.Gateway, CAPin: created.Token.CAPin})
	stop := h.agent(t, store, newFakeCluster())
	eventually(t, "the cluster to enrol and connect", func() bool {
		c, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected
	})
	stop()
	if err := runner.Dispatch(ctx, job); !isRefused(err, build.CodeNoBuilder) {
		t.Fatalf("dispatch to a cluster without a builder: %v", err)
	}

	// the agent with a builder
	conn := dialAs(t, h, store.Identity())
	client := fleetv1.NewFleetClient(conn)
	open := func() (fleetv1.Fleet_ConnectClient, <-chan *fleetv1.ControlMessage, context.CancelFunc) {
		t.Helper()
		sctx, cancel := context.WithCancel(ctx)
		stream, err := client.Connect(sctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_Hello{Hello: &fleetv1.Hello{AgentVersion: "test",
			Capabilities: &fleetv1.Capabilities{KubernetesVersion: "v1.34.0+test",
				Builder: &fleetv1.Builder{Registry: "zot.odyssey-system.svc:5000", Platforms: []string{"linux/amd64"}}}}}}); err != nil {
			t.Fatal(err)
		}
		control := make(chan *fleetv1.ControlMessage, 16)
		go func() {
			for {
				m, err := stream.Recv()
				if err != nil {
					close(control)
					return
				}
				control <- m
			}
		}()
		return stream, control, cancel
	}
	stream, control, cancel := open()
	defer func() { cancel() }()
	eventually(t, "the builder to be announced", func() bool {
		c, err := f.Queries.Find(ctx, org, created.Cluster.ID)
		return err == nil && c.Connected && c.Agent.Capabilities.Builder != nil
	})

	if err := runner.Dispatch(ctx, job); err != nil {
		t.Fatal(err)
	}
	got := next(t, control, func(m *fleetv1.ControlMessage) bool { return m.GetBuild() != nil }).GetBuild()
	repo := "zot.odyssey-system.svc:5000/" + org.String() + "/" + b.Project.String()
	if got.GetBuildId() != b.ID.String() || got.GetImageRepository() != repo || got.GetCacheRef() != repo+":buildcache" ||
		got.GetSource().GetSha256() != source.SHA256 || got.GetSource().GetSize() != source.Size || got.GetTimeoutSeconds() != int32(buildfleet.Deadline.Seconds()) {
		t.Fatalf("build sent %+v", got)
	}
	if o, err := runner.Observe(ctx, b); err != nil || o.Phase != build.PhasePending {
		t.Fatalf("observed before any report: %+v %v", o, err)
	}

	// the source, over the agent's own certificate
	if fetched := fetch(t, client, b.ID.String()); !bytes.Equal(fetched, src) {
		t.Fatalf("fetched %d bytes, uploaded %d", len(fetched), len(src))
	}
	if _, err := fetchErr(client, uuid.NewString()); status.Code(err) != codes.NotFound {
		t.Fatalf("fetch an unknown build: %v", err)
	}
	// a stored source that no longer matches what was uploaded is never served whole
	tampered := build.Job{Build: build.Build{ID: build.NewID(), Organization: org, Project: b.Project, Environment: b.Environment},
		Source: build.Source{Organization: org, Size: source.Size, SHA256: strings.Repeat("0", 64), Key: source.Key}}
	if err := runner.Dispatch(ctx, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchErr(client, tampered.Build.ID.String()); status.Code(err) != codes.DataLoss {
		t.Fatalf("fetch a tampered source: %v", err)
	}

	// logs and the result
	send := func(m *fleetv1.AgentMessage) {
		t.Helper()
		if err := stream.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildReport{BuildReport: &fleetv1.BuildReport{
		Builds: []*fleetv1.BuildStatus{{BuildId: b.ID.String(), Phase: "running"}}}}})
	lines := &fleetv1.BuildLog{BuildId: b.ID.String(), Lines: []*fleetv1.LogLine{
		{Seq: 1, Stream: "prepare", Text: "source verified"}, {Seq: 2, Stream: "build", Text: "#1 load build definition"}}}
	send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildLog{BuildLog: lines}})
	send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildLog{BuildLog: lines}}) // a resend after a reconnect
	eventually(t, "the build to run with its log", func() bool {
		o, _ := runner.Observe(ctx, b)
		l, _ := runner.Logs(ctx, b, 0, 100)
		return o.Phase == build.PhaseRunning && len(l) == 2 && l[1].Text == "#1 load build definition"
	})
	digest := "sha256:" + strings.Repeat("d", 64)
	send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildReport{BuildReport: &fleetv1.BuildReport{
		Builds: []*fleetv1.BuildStatus{{BuildId: b.ID.String(), Phase: "succeeded", ImageDigest: digest}}}}})
	eventually(t, "the build to succeed", func() bool {
		o, _ := runner.Observe(ctx, b)
		return o.Phase == build.PhaseSucceeded && o.Image == repo+"@"+digest
	})
	if _, err := fetchErr(client, b.ID.String()); status.Code(err) != codes.NotFound {
		t.Fatalf("fetch a finished build's source: %v", err)
	}

	// archiving lets the cluster go of it and drops the stored lines
	if err := runner.Cancel(ctx, b); err != nil {
		t.Fatal(err)
	}
	if m := next(t, control, func(m *fleetv1.ControlMessage) bool { return m.GetCancelBuild() != nil }); m.GetCancelBuild().GetBuildId() != b.ID.String() {
		t.Fatalf("cancel %+v", m)
	}
	if err := runner.Forget(ctx, b); err != nil {
		t.Fatal(err)
	}
	if l, err := runner.Logs(ctx, b, 0, 100); err != nil || len(l) != 0 {
		t.Fatalf("logs after forgetting: %+v %v", l, err)
	}

	// a reconnected agent is told everything again: the wanted build to
	// build, the let-go one to drop
	cancel()
	_, control, cancel = open()
	resent := map[string]string{}
	for len(resent) < 2 {
		m := next(t, control, func(m *fleetv1.ControlMessage) bool { return m.GetBuild() != nil || m.GetCancelBuild() != nil })
		if m.GetBuild() != nil {
			resent[m.GetBuild().GetBuildId()] = "build"
		} else {
			resent[m.GetCancelBuild().GetBuildId()] = "cancel"
		}
	}
	if resent[tampered.Build.ID.String()] != "build" || resent[b.ID.String()] != "cancel" {
		t.Fatalf("resent on reconnect %v", resent)
	}
	// a cancelled build's source is not served
	if err := runner.Cancel(ctx, tampered.Build); err != nil {
		t.Fatal(err)
	}
	next(t, control, func(m *fleetv1.ControlMessage) bool {
		return m.GetCancelBuild().GetBuildId() == tampered.Build.ID.String()
	})
	if _, err := fetchErr(client, tampered.Build.ID.String()); status.Code(err) != codes.NotFound {
		t.Fatalf("fetch a cancelled build's source: %v", err)
	}

	// another organization's build id fetches nothing
	other := build.Build{ID: build.NewID(), Organization: identity.NewOrganizationID()}
	if o, err := runner.Observe(ctx, other); err != nil || o.Phase != build.PhasePending {
		t.Fatalf("observe an unknown build: %+v %v", o, err)
	}
}

func isRefused(err error, code string) bool {
	var e *errx.Error
	return errors.As(err, &e) && e.Type == errx.TypeBusiness && e.Code == code
}

func next(t *testing.T, c <-chan *fleetv1.ControlMessage, match func(*fleetv1.ControlMessage) bool) *fleetv1.ControlMessage {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-c:
			if !ok {
				t.Fatal("the tunnel closed")
			}
			if match(m) {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for a control message")
		}
	}
}

func fetch(t *testing.T, client fleetv1.FleetClient, id string) []byte {
	t.Helper()
	data, err := fetchErr(client, id)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fetchErr(client fleetv1.FleetClient, id string) ([]byte, error) {
	s, err := client.FetchSource(context.Background(), &fleetv1.FetchSourceRequest{BuildId: id})
	if err != nil {
		return nil, err
	}
	var out []byte
	for {
		c, err := s.Recv()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c.GetData()...)
	}
}
