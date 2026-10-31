package tunnel

import (
	"context"
	"errors"
	"io"
	"time"

	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/util/workqueue"
)

// Builds is what the tunnel does with builds in the cluster (agent/builds).
type Builds interface {
	// Start creates the build (once); a non-nil status is a refusal.
	Start(ctx context.Context, b *fleetv1.Build) (*fleetv1.BuildStatus, error)
	Cancel(ctx context.Context, id string) error
	// Status is nil for a build the cluster does not have.
	Status(ctx context.Context, id string) (*fleetv1.BuildStatus, error)
	Statuses(ctx context.Context) ([]*fleetv1.BuildStatus, error)
	// FollowLogs sends the build's log until it is complete.
	FollowLogs(ctx context.Context, id string, send func([]*fleetv1.LogLine) error) error
}

// FinalGrace is how long a final status waits for its log to be sent (the
// control plane archives the log when it sees the final status).
const FinalGrace = 30 * time.Second

// maxReportBuilds matches the gateway's limit (fleet.MaxReportBuilds).
const maxReportBuilds = 200

// buildOp is what the control plane last asked of a build.
type buildOp struct {
	start  *fleetv1.Build // nil: cancel
	cancel bool
}

// follower is one build's log, followed while a tunnel is up.
type follower struct {
	done    chan struct{}
	finalAt time.Time // when its final status was first held back
}

// receiveBuild queues a build or its cancellation.
func (c *Client) receiveBuild(m *fleetv1.ControlMessage) bool {
	var id string
	var op buildOp
	switch {
	case m.GetBuild() != nil:
		id, op = m.GetBuild().GetBuildId(), buildOp{start: m.GetBuild()}
	case m.GetCancelBuild() != nil:
		id, op = m.GetCancelBuild().GetBuildId(), buildOp{cancel: true}
	default:
		return false
	}
	if c.o.Builds == nil || id == "" {
		return true // this agent does not build (it did not say it could)
	}
	c.mu.Lock()
	if cur, ok := c.buildOps[id]; !ok || !cur.cancel { // a cancellation is never undone
		c.buildOps[id] = op
	}
	c.mu.Unlock()
	c.buildQueue.Add(id)
	return true
}

// workBuilds starts and cancels queued builds (transient errors retry).
func (c *Client) workBuilds(ctx context.Context) {
	for {
		id, shutdown := c.buildQueue.Get()
		if shutdown {
			return
		}
		c.mu.Lock()
		op, ok := c.buildOps[id]
		c.mu.Unlock()
		if ok {
			var err error
			var refused *fleetv1.BuildStatus
			if op.cancel {
				err = c.o.Builds.Cancel(ctx, id)
			} else {
				refused, err = c.o.Builds.Start(ctx, op.start)
			}
			switch {
			case err == nil:
				c.mu.Lock()
				if c.buildOps[id] == op {
					delete(c.buildOps, id)
				}
				if op.cancel {
					delete(c.refusedBuilds, id)
				} else if refused != nil {
					c.o.Log.Error(errors.New(refused.GetMessage()), "build refused", "build", id)
					c.refusedBuilds[id] = refused
				}
				c.mu.Unlock()
				c.buildQueue.Forget(id)
				if !op.cancel {
					c.BuildChanged(id)
				}
			case ctx.Err() == nil:
				c.o.Log.Info("build request failed, retrying", "build", id, "cancel", op.cancel, "err", err)
				c.buildQueue.AddRateLimited(id)
			}
		}
		c.buildQueue.Done(id)
	}
}

// BuildChanged marks a build whose status may have changed (the agent's
// watch on OdysseyBuilds calls it).
func (c *Client) BuildChanged(id string) {
	c.mu.Lock()
	c.buildsChanged[id] = true
	c.mu.Unlock()
	c.poke()
}

func (c *Client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// buildSession is the per-stream state of builds: their log followers.
type buildSession struct {
	ctx       context.Context
	send      func(*fleetv1.AgentMessage) error
	followers map[string]*follower // guarded by Client.mu
}

func (c *Client) newBuildSession(ctx context.Context, send func(*fleetv1.AgentMessage) error) *buildSession {
	return &buildSession{ctx: ctx, send: send, followers: map[string]*follower{}}
}

// follow starts following a build's log on this stream (once).
func (c *Client) follow(s *buildSession, id string) *follower {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := s.followers[id]; f != nil {
		return f
	}
	f := &follower{done: make(chan struct{})}
	s.followers[id] = f
	go func() {
		defer close(f.done)
		err := c.o.Builds.FollowLogs(s.ctx, id, func(lines []*fleetv1.LogLine) error {
			return s.send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildLog{BuildLog: &fleetv1.BuildLog{BuildId: id, Lines: lines}}})
		})
		if err != nil && s.ctx.Err() == nil {
			c.o.Log.Info("build log not followed to its end", "build", id, "err", err)
		}
		c.BuildChanged(id) // a final status held back for it may go now
	}()
	return f
}

// ready reports whether a build's status may be sent: a final one waits
// for its log (up to FinalGrace); then is how long until it may.
func (c *Client) ready(s *buildSession, st *fleetv1.BuildStatus) (ok bool, then time.Duration) {
	f := c.follow(s, st.GetBuildId())
	if st.GetPhase() != "succeeded" && st.GetPhase() != "failed" {
		return true, 0
	}
	select {
	case <-f.done:
		return true, 0
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f.finalAt.IsZero() {
		f.finalAt = time.Now()
	}
	left := FinalGrace - time.Since(f.finalAt)
	return left <= 0, left
}

// reportBuilds sends the statuses of the changed builds (all: every build
// the cluster has, plus the refused ones).
func (c *Client) reportBuilds(ctx context.Context, s *buildSession, all bool) error {
	if c.o.Builds == nil {
		return nil
	}
	var statuses []*fleetv1.BuildStatus
	c.mu.Lock()
	changed := c.buildsChanged
	c.buildsChanged = map[string]bool{}
	c.mu.Unlock()
	if all {
		list, err := c.o.Builds.Statuses(ctx)
		if err != nil {
			c.o.Log.Info("read build statuses", "err", err)
		}
		statuses = list
		c.mu.Lock()
		for _, r := range c.refusedBuilds {
			statuses = append(statuses, r)
		}
		c.mu.Unlock()
	} else {
		for id := range changed {
			c.mu.Lock()
			st := c.refusedBuilds[id]
			c.mu.Unlock()
			if st == nil {
				var err error
				if st, err = c.o.Builds.Status(ctx, id); err != nil {
					c.o.Log.Info("read build status", "build", id, "err", err)
					c.later(id, time.Second)
					continue
				}
			}
			if st != nil {
				statuses = append(statuses, st)
			}
		}
	}
	out := statuses[:0]
	for _, st := range statuses {
		if _, refused := c.refused(st.GetBuildId()); refused {
			out = append(out, st)
			continue
		}
		ok, then := c.ready(s, st)
		if !ok {
			c.later(st.GetBuildId(), then)
			continue
		}
		out = append(out, st)
	}
	for len(out) > 0 {
		n := min(len(out), maxReportBuilds)
		if err := s.send(&fleetv1.AgentMessage{Message: &fleetv1.AgentMessage_BuildReport{BuildReport: &fleetv1.BuildReport{Builds: out[:n]}}}); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

func (c *Client) refused(id string) (*fleetv1.BuildStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.refusedBuilds[id]
	return r, ok
}

// later reports the build again after d.
func (c *Client) later(id string, d time.Duration) {
	time.AfterFunc(max(d, 100*time.Millisecond), func() { c.BuildChanged(id) })
}

// ---- sources ----------------------------------------------------------

// Errors of FetchSource.
var (
	// ErrNoTunnel: this replica holds no tunnel (it is not the leader, or the
	// tunnel is down); the caller retries.
	ErrNoTunnel = errors.New("the tunnel to the control plane is not up on this replica")
	// ErrSourceNotFound: the control plane has no source for the build.
	ErrSourceNotFound = errors.New("the control plane has no source for this build")
	// ErrSourceBusy: too many sources are being sent; the caller retries.
	ErrSourceBusy = errors.New("the control plane is sending too many sources of this cluster")
)

// FetchSource opens a build's source through the tunnel's connection. The
// control plane checks it as it sends it: a source that does not match
// fails the read before its last bytes.
func (c *Client) FetchSource(ctx context.Context, id string) (io.ReadCloser, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, ErrNoTunnel
	}
	ctx, cancel := context.WithCancel(ctx)
	st, err := fleetv1.NewFleetClient(conn).FetchSource(ctx, &fleetv1.FetchSourceRequest{BuildId: id})
	if err == nil {
		var first *fleetv1.SourceChunk
		if first, err = st.Recv(); err == nil || errors.Is(err, io.EOF) {
			r := &sourceReader{st: st, cancel: cancel, buf: first.GetData()}
			if errors.Is(err, io.EOF) {
				r.err = io.EOF
			}
			return r, nil
		}
	}
	cancel()
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument:
		return nil, ErrSourceNotFound
	case codes.ResourceExhausted:
		return nil, ErrSourceBusy
	}
	return nil, err
}

type sourceReader struct {
	st     grpc.ServerStreamingClient[fleetv1.SourceChunk]
	cancel context.CancelFunc
	buf    []byte
	err    error
}

func (r *sourceReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		m, err := r.st.Recv()
		if err != nil {
			r.err = err
			continue
		}
		r.buf = m.GetData()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *sourceReader) Close() error {
	r.cancel()
	return nil
}

func newBuildQueue() workqueue.TypedRateLimitingInterface[string] {
	return workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.NewTypedItemExponentialFailureRateLimiter[string](500*time.Millisecond, 30*time.Second),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "odyssey-builds"})
}
