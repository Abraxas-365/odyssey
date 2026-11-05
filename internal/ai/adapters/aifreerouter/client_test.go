package aifreerouter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/ai/adapters/aifreerouter"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

type call struct{ method, path, auth, body string }

func server(t *testing.T, h http.HandlerFunc) (*aifreerouter.Client, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return aifreerouter.New(aifreerouter.Options{URL: srv.URL + "/", Key: "ik_svc_platform"}), &calls
}

func TestCreateKeyAsksForAnInvokeOnlyKeyThatNeverExpires(t *testing.T) {
	c, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sa-9","secret":"ik_svc_new","expires_at":null}`))
	})
	k, err := c.CreateKey(context.Background(), "odicea-env-1")
	if err != nil || k.ID != "sa-9" || k.Secret != "ik_svc_new" {
		t.Fatalf("%+v %v", k, err)
	}
	got := (*calls)[0]
	if got.method != http.MethodPost || got.path != "/api/v1/service-accounts" || got.auth != "Bearer ik_svc_platform" {
		t.Errorf("request: %+v", got)
	}
	var body struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		ExpiresIn   string   `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "odicea-env-1" || len(body.Permissions) != 1 || body.Permissions[0] != "freerouter:gateway:invoke" || body.ExpiresIn != "never" {
		t.Errorf("body: %+v", body)
	}
}

func TestCreateKeyRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"platform key refused": {http.StatusForbidden, `{"code":"FORBIDDEN","message":"nope"}`, "refused the platform's key"},
		"down":                 {http.StatusBadGateway, ``, "not available"},
		"throttled":            {http.StatusTooManyRequests, ``, "not available"},
		"bad request":          {http.StatusBadRequest, `{"code":"VALIDATION","message":"name taken"}`, "name taken"},
		"bad request (nested)": {http.StatusBadRequest, `{"error":{"code":"VALIDATION","message":"name taken in middleware"}}`, "name taken in middleware"},
		"nested, code only":    {http.StatusConflict, `{"error":{"code":"CONFLICT"}}`, "CONFLICT"},
		"unauthorized nested":  {http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`, "refused the platform's key"},
		"no secret":            {http.StatusCreated, `{"id":"x"}`, "without a key"},
		"garbage":              {http.StatusCreated, `<html>`, "unexpected"},
	} {
		cl, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(c.status); _, _ = w.Write([]byte(c.body)) })
		_, err := cl.CreateKey(context.Background(), "n")
		if !errx.IsType(err, errx.TypeExternal) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "ik_svc_platform") {
			t.Errorf("%s: the platform key leaked into the error", name)
		}
	}
}

func TestRevokeKey(t *testing.T) {
	status := http.StatusNoContent
	c, calls := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	if err := c.RevokeKey(context.Background(), "sa 1/x"); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[0]; got.method != http.MethodDelete || got.path != "/api/v1/service-accounts/sa 1/x" && got.path != "/api/v1/service-accounts/sa%201%2Fx" {
		t.Errorf("request: %+v", got)
	}
	status = http.StatusNotFound // already gone: success
	if err := c.RevokeKey(context.Background(), "sa-1"); err != nil {
		t.Errorf("revoking a revoked key: %v", err)
	}
	status = http.StatusInternalServerError
	if err := c.RevokeKey(context.Background(), "sa-1"); err == nil {
		t.Error("a 500 must be retried")
	}
	n := len(*calls)
	if err := c.RevokeKey(context.Background(), ""); err != nil || len(*calls) != n {
		t.Error("an empty id makes no call")
	}
}

func TestListKeysFiltersByNamePrefix(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"1","name":"odicea-e1-a"},{"id":"2","name":"odicea-e2-a"},{"id":"3","name":"someone-else"},{"id":"4","name":"odicea-e1-b"}]`))
	})
	got, err := c.ListKeys(context.Background(), "odicea-e1-")
	if err != nil || len(got) != 2 || got[0].ID != "1" || got[1].ID != "4" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestUnreachableGateway(t *testing.T) {
	c := aifreerouter.New(aifreerouter.Options{URL: "http://127.0.0.1:1", Key: "ik_svc_platform"})
	_, err := c.CreateKey(context.Background(), "n")
	if !errx.IsType(err, errx.TypeExternal) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("%v", err)
	}
}
