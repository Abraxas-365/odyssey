package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	buildctl "github.com/Abraxas-365/odyssey/agent/controllers/build"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const id = "0b5c4a5e-3d4f-4a7b-9c1d-2e3f4a5b6c7d"

var body = []byte("the source archive")

func objects(phase v1alpha1.BuildPhase, owner bool) []client.Object {
	b := &v1alpha1.OdysseyBuild{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: buildctl.Namespace, UID: "u1"},
		Spec:       v1alpha1.OdysseyBuildSpec{Source: v1alpha1.BuildSource{SHA256: strings.Repeat("a", 64), Size: int64(len(body))}},
		Status:     v1alpha1.OdysseyBuildStatus{Phase: phase},
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: buildctl.JobName(b), Namespace: buildctl.Namespace},
		Data: map[string][]byte{buildctl.TokenKey: []byte("secret-token")}}
	if owner {
		yes := true
		sec.OwnerReferences = []metav1.OwnerReference{{UID: "u1", Controller: &yes, Name: id, Kind: "OdysseyBuild", APIVersion: "odyssey.dev/v1alpha1"}}
	}
	return []client.Object{b, sec}
}

func server(t *testing.T, objs []client.Object, fetch Fetch) *Server {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return New(c, fetch, logr.Discard())
}

func get(s *Server, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil && r != http.ErrAbortHandler {
				panic(r)
			} else if r != nil {
				w.Code = -1 // the response was cut
			}
		}()
		s.ServeHTTP(w, req)
	}()
	return w
}

func fetchOf(data []byte, err error) Fetch {
	return func(context.Context, string) (io.ReadCloser, error) {
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

func TestServesTheSourceToItsBuild(t *testing.T) {
	var asked string
	s := server(t, objects(v1alpha1.BuildRunning, true), func(_ context.Context, b string) (io.ReadCloser, error) {
		asked = b
		return io.NopCloser(bytes.NewReader(body)), nil
	})
	w := get(s, "/builds/"+id+"/source", "secret-token")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) || asked != id || w.Header().Get("Content-Length") != "18" {
		t.Fatalf("%d %q %q", w.Code, w.Body.String(), asked)
	}
}

func TestRefusals(t *testing.T) {
	ok := fetchOf(body, nil)
	cases := []struct {
		name  string
		objs  []client.Object
		fetch Fetch
		path  string
		token string
		code  int
	}{
		{"no token", objects(v1alpha1.BuildRunning, true), ok, "/builds/" + id + "/source", "", http.StatusUnauthorized},
		{"wrong token", objects(v1alpha1.BuildRunning, true), ok, "/builds/" + id + "/source", "secret-tokeN", http.StatusUnauthorized},
		{"a Secret it does not own", objects(v1alpha1.BuildRunning, false), ok, "/builds/" + id + "/source", "secret-token", http.StatusUnauthorized},
		{"unknown build (as a wrong token)", nil, ok, "/builds/" + id + "/source", "secret-token", http.StatusUnauthorized},
		{"bad path", objects(v1alpha1.BuildRunning, true), ok, "/builds/../source", "secret-token", http.StatusNotFound},
		{"over", objects(v1alpha1.BuildSucceeded, true), ok, "/builds/" + id + "/source", "secret-token", http.StatusGone},
		{"not in the control plane", objects(v1alpha1.BuildRunning, true), fetchOf(nil, ErrNotFound), "/builds/" + id + "/source", "secret-token", http.StatusNotFound},
		{"busy", objects(v1alpha1.BuildRunning, true), fetchOf(nil, ErrBusy), "/builds/" + id + "/source", "secret-token", http.StatusTooManyRequests},
		{"no tunnel here", objects(v1alpha1.BuildRunning, true), fetchOf(nil, ErrUnavailable), "/builds/" + id + "/source", "secret-token", http.StatusServiceUnavailable},
		{"tunnel broken", objects(v1alpha1.BuildRunning, true), fetchOf(nil, errors.New("eof")), "/builds/" + id + "/source", "secret-token", http.StatusBadGateway},
		{"short stream", objects(v1alpha1.BuildRunning, true), fetchOf(body[:5], nil), "/builds/" + id + "/source", "secret-token", -1},
		{"long stream", objects(v1alpha1.BuildRunning, true), fetchOf(append(body, 'x'), nil), "/builds/" + id + "/source", "secret-token", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := get(server(t, c.objs, c.fetch), c.path, c.token); w.Code != c.code {
				t.Fatalf("got %d, want %d (%s)", w.Code, c.code, w.Body.String())
			}
		})
	}
}

func TestLimitsConcurrentSources(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, MaxConcurrent)
	s := server(t, objects(v1alpha1.BuildRunning, true), func(ctx context.Context, _ string) (io.ReadCloser, error) {
		started <- struct{}{}
		<-release
		return io.NopCloser(bytes.NewReader(body)), nil
	})
	done := make(chan int, MaxConcurrent)
	for range MaxConcurrent {
		go func() { done <- get(s, "/builds/"+id+"/source", "secret-token").Code }()
	}
	for range MaxConcurrent {
		<-started
	}
	if w := get(s, "/builds/"+id+"/source", "secret-token"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d", w.Code)
	}
	close(release)
	for range MaxConcurrent {
		if code := <-done; code != http.StatusOK {
			t.Fatalf("code %d", code)
		}
	}
}

// A pod guessing tokens is slowed down before any API read; another
// address is not.
func TestLimitsEachAddress(t *testing.T) {
	s := server(t, objects(v1alpha1.BuildRunning, true), fetchOf(body, nil))
	for i := range PerAddressBurst {
		if w := get(s, "/builds/"+id+"/source", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := get(s, "/builds/"+id+"/source", "secret-token"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("over the burst: %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/builds/"+id+"/source", nil)
	req.RemoteAddr = "10.0.0.9:4000"
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("another address: %d", w.Code)
	}
}
