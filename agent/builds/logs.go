package builds

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LogOpener opens a container's log, following it while it runs.
type LogOpener func(ctx context.Context, namespace, pod, container string) (io.ReadCloser, error)

// Log streams, in the order the pod runs them, and the seq space of each: a
// line's seq is its stream's index << 32 | its line number (from 1), so a
// log read again from the start (another tunnel, a broken stream) gives
// every line its old seq and the control plane keeps it once.
var logStreams = []struct {
	container, stream string
	index             int64
}{
	{buildctl.ContainerPrepare, "prepare", 1},
	{buildctl.ContainerBuild, "build", 2},
}

const (
	// maxLine is what is kept of a line (the gateway keeps 4 KiB).
	maxLine = 4 << 10
	// batchLines and batchEvery bound a BuildLog message.
	batchLines = 500
	batchEvery = 250 * time.Millisecond
	// pollEvery is how often a container that has not started is checked.
	pollEvery = time.Second
	// openAttempts bounds the tries to read an ended container's log.
	openAttempts = 5
)

// FollowLogs sends the build's log as it is written and returns nil once it
// is complete: every container that ran has ended and its log was read to
// the end (or can no longer be read: the pod is gone). It returns early with
// ctx's error or send's.
func (m *Manager) FollowLogs(ctx context.Context, id string, send func([]*fleetv1.LogLine) error) error {
	if !idPattern.MatchString(id) || m.Logs == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &batcher{send: send}
	go b.tick(ctx)
	for _, s := range logStreams {
		ran, err := m.follow(ctx, id, s.container, s.index, s.stream, b)
		if err != nil {
			return err
		}
		if !ran {
			break // a container that never ran: the next one will not either
		}
	}
	return b.flush()
}

// follow reads one container's log to its end; ran is false when the
// container never started (the build ended or was deleted before).
func (m *Manager) follow(ctx context.Context, id, container string, index int64, stream string, b *batcher) (bool, error) {
	var read int64 // lines sent (counted from the start of the log)
	failures := 0
	for {
		pod, cs, final, err := m.container(ctx, id, container)
		if err != nil {
			return read > 0, err
		}
		started := cs != nil && (cs.State.Running != nil || cs.State.Terminated != nil)
		switch {
		case pod == nil && (final || read > 0):
			return read > 0, nil // gone: what was read is all there is
		case !started && final:
			return read > 0, nil
		case !started:
			if err := sleep(ctx, pollEvery); err != nil {
				return read > 0, err
			}
			continue
		}
		ended := cs.State.Terminated != nil
		// a build that is over whose container still shows running (its node
		// is gone, the cache lags) is not waited on forever
		giveUp := func() bool { failures++; return (ended || final) && failures >= openAttempts }
		rc, err := m.Logs(ctx, buildctl.Namespace, pod.Name, container)
		switch {
		case apierrors.IsNotFound(err):
			return true, nil
		case err != nil && ctx.Err() != nil:
			return true, ctx.Err()
		case err != nil:
			if giveUp() {
				return true, nil // its log cannot be read (its node is gone)
			}
			if err := sleep(ctx, pollEvery); err != nil {
				return true, err
			}
			continue
		}
		err = readLines(rc, ended, func(n int64, text string) error {
			if n <= read {
				return nil // sent before the stream broke
			}
			read = n
			return b.add(&fleetv1.LogLine{Seq: index<<32 | n, Stream: stream, Text: text})
		})
		rc.Close()
		if ferr := b.err(); ferr != nil || ctx.Err() != nil {
			return true, errors.Join(ferr, ctx.Err())
		}
		if ended && err == nil {
			return true, nil // it had ended: its log was read whole
		}
		// The stream ended while the container ran (it ended meanwhile, or
		// the connection broke): look again, skipping what was sent.
		if (err != nil || final) && giveUp() {
			return true, nil
		}
		if err := sleep(ctx, pollEvery/4); err != nil {
			return true, err
		}
	}
}

// container is the build's newest pod and the named container's status in
// it; final reports whether the build is over (or gone).
func (m *Manager) container(ctx context.Context, id, name string) (*corev1.Pod, *corev1.ContainerStatus, bool, error) {
	var ob v1alpha1.OdysseyBuild
	err := m.Client.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &ob)
	if apierrors.IsNotFound(err) {
		return nil, nil, true, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	final := ob.Status.Phase.Final() || !ob.DeletionTimestamp.IsZero()
	var pods corev1.PodList
	if err := m.Client.List(ctx, &pods, client.InNamespace(buildctl.Namespace), client.MatchingLabels{buildctl.LabelBuild: id}); err != nil {
		return nil, nil, final, err
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if p := &pods.Items[i]; pod == nil || pod.CreationTimestamp.Before(&p.CreationTimestamp) {
			pod = p
		}
	}
	if pod == nil {
		return nil, nil, final, nil
	}
	for _, list := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for i := range list {
			if list[i].Name == name {
				return pod, &list[i], final, nil
			}
		}
	}
	return pod, nil, final, nil
}

// readLines calls fn with each line of r, numbered from 1 and cut to
// maxLine bytes; it returns nil at EOF. A last line without its newline is
// sent only when whole (the container had ended) and at a clean EOF: else
// it may be cut, and the next read sends it whole under the same number.
func readLines(r io.Reader, whole bool, fn func(n int64, text string) error) error {
	br := bufio.NewReaderSize(r, 16<<10)
	var n int64
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line) < maxLine {
			line = append(line, chunk[:min(len(chunk), maxLine-len(line))]...)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue // a long line: keep reading it (its excess is dropped)
		case err == nil:
			n++
			if err := fn(n, strings.TrimRight(string(line), "\r\n")); err != nil {
				return err
			}
			line = line[:0]
		case errors.Is(err, io.EOF):
			if len(line) > 0 && whole {
				n++
				if err := fn(n, strings.TrimRight(string(line), "\r\n")); err != nil {
					return err
				}
			}
			return nil
		default:
			return err
		}
	}
}

// batcher sends lines in messages of at most batchLines; tick sends what
// waits every batchEvery.
type batcher struct {
	send func([]*fleetv1.LogLine) error

	mu     sync.Mutex
	lines  []*fleetv1.LogLine
	failed error
}

func (b *batcher) add(l *fleetv1.LogLine) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed != nil {
		return b.failed
	}
	b.lines = append(b.lines, l)
	if len(b.lines) >= batchLines {
		b.flushLocked()
	}
	return b.failed
}

func (b *batcher) flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
	return b.failed
}

func (b *batcher) flushLocked() {
	if b.failed != nil || len(b.lines) == 0 {
		return
	}
	lines := b.lines
	b.lines = nil
	b.failed = b.send(lines)
}

func (b *batcher) err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failed
}

func (b *batcher) tick(ctx context.Context) {
	t := time.NewTicker(batchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = b.flush()
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
