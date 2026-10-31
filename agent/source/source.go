// Package source serves a build's pod its source: the pod asks the agent
// (the only one holding the tunnel's certificate) with its one-time token,
// and the agent relays the control plane's FetchSource stream, in memory.
package source

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Fetch opens a build's source from the control plane (the tunnel).
type Fetch func(ctx context.Context, build string) (io.ReadCloser, error)

// ErrNotFound is what Fetch returns when the control plane has no source
// for the build (cancelled, finished, unknown).
var ErrNotFound = errors.New("the control plane has no source for this build")

// ErrBusy is what Fetch returns when too many sources are being sent.
var ErrBusy = errors.New("too many sources are being fetched")

// ErrUnavailable is what Fetch returns when this replica cannot reach the
// control plane now (it holds no tunnel); the pod tries again.
var ErrUnavailable = errors.New("this agent replica holds no tunnel to the control plane")

// MaxConcurrent matches the gateway's per-cluster limit.
const MaxConcurrent = 4

const (
	// SendWithin bounds sending one source (the largest, 100 MiB, at a
	// slow ~1 MiB/s).
	SendWithin = 3 * time.Minute
	// PerAddressBurst requests an address may make at once (then one a
	// second): a pod's retries fit.
	PerAddressBurst = 10
	maxAddresses    = 4096
)

// Server is the http.Handler of GET /builds/{id}/source.
type Server struct {
	// Reader reads builds and their token Secrets (the API, uncached).
	Reader client.Reader
	Fetch  Fetch
	Log    logr.Logger

	slots  chan struct{}
	mu     sync.Mutex
	limits map[string]*rate.Limiter
}

var path = regexp.MustCompile(`^/builds/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/source$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m := path.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	id := m[1]
	if !s.allow(r.RemoteAddr) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	b, code := s.authorize(r.Context(), id, token)
	if code != http.StatusOK {
		http.Error(w, http.StatusText(code), code)
		return
	}
	select {
	case s.slot() <- struct{}{}:
		defer func() { <-s.slot() }()
	default:
		http.Error(w, ErrBusy.Error(), http.StatusTooManyRequests)
		return
	}
	// a pod that stops reading must not hold a slot (and its stream)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(SendWithin))
	src, err := s.Fetch(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, ErrBusy):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	case errors.Is(err, ErrUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		s.Log.Info("fetch source", "build", id, "err", err)
		http.Error(w, "the control plane cannot be reached", http.StatusBadGateway)
		return
	}
	defer src.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(b.Spec.Source.Size, 10))
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(w, io.LimitReader(src, b.Spec.Source.Size+1))
	if err != nil || n != b.Spec.Source.Size {
		// the pod sees a cut response (its own checks fail the build)
		s.Log.Info("source cut", "build", id, "sent", n, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) slot() chan struct{} { return s.slots }

// allow limits each address's requests (every one costs two API reads
// before its token is known good; any pod in odyssey-builds can ask).
func (s *Server) allow(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits == nil || len(s.limits) > maxAddresses {
		s.limits = map[string]*rate.Limiter{} // forget them all: it only resets the limits
	}
	l := s.limits[host]
	if l == nil {
		l = rate.NewLimiter(rate.Every(time.Second), PerAddressBurst)
		s.limits[host] = l
	}
	return l.Allow()
}

// authorize finds the build and checks the token of its Secret; a build
// that is over has no source to give. An unknown build and a wrong token
// answer the same (a pod learns nothing of other builds).
func (s *Server) authorize(ctx context.Context, id, token string) (*v1alpha1.OdysseyBuild, int) {
	var b v1alpha1.OdysseyBuild
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: id}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, http.StatusUnauthorized
		}
		return nil, http.StatusServiceUnavailable
	}
	var sec corev1.Secret
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: buildctl.Namespace, Name: buildctl.JobName(&b)}, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, http.StatusUnauthorized
		}
		return nil, http.StatusServiceUnavailable
	}
	want := sec.Data[buildctl.TokenKey]
	owned := false
	for _, o := range sec.OwnerReferences {
		owned = owned || (o.UID == b.UID && o.Controller != nil && *o.Controller)
	}
	if len(want) == 0 || !owned || subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return nil, http.StatusUnauthorized
	}
	if b.Status.Phase.Final() || !b.DeletionTimestamp.IsZero() {
		return nil, http.StatusGone
	}
	return &b, http.StatusOK
}

// Runnable serves on addr until ctx ends (a manager.Runnable; every replica
// serves, but only the leader's tunnel can fetch).
type Runnable struct {
	Addr   string
	Server *Server
}

func (r Runnable) Start(ctx context.Context) error {
	if r.Server.slots == nil {
		r.Server.slots = make(chan struct{}, MaxConcurrent)
	}
	srv := &http.Server{Addr: r.Addr, Handler: r.Server, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// NeedLeaderElection: the Service reaches any replica, so every one serves.
func (Runnable) NeedLeaderElection() bool { return false }

// New is a Server ready to use.
func New(reader client.Reader, fetch Fetch, log logr.Logger) *Server {
	return &Server{Reader: reader, Fetch: fetch, Log: log, slots: make(chan struct{}, MaxConcurrent)}
}
