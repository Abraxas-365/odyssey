package sourcegithub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManifestShape(t *testing.T) {
	raw, err := Manifest(ManifestOptions{Name: "Odicea", PublicURL: "http://localhost:8095/"}, "http://127.0.0.1:5555/callback")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":                     "Odicea",
		"url":                      "http://localhost:8095",
		"redirect_url":             "http://127.0.0.1:5555/callback",
		"setup_url":                "http://localhost:8095/github/setup",
		"request_oauth_on_install": true,
		"setup_on_update":          true,
		"public":                   false,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	hook := m["hook_attributes"].(map[string]any)
	if hook["url"] != "http://localhost:8095/webhooks/github" || hook["active"] != true {
		t.Errorf("hook_attributes = %v", hook)
	}
	if cb := m["callback_urls"].([]any); len(cb) != 1 || cb[0] != "http://localhost:8095/github/setup" {
		t.Errorf("callback_urls = %v", cb)
	}
	perms := m["default_permissions"].(map[string]any)
	if len(perms) != 4 || perms["contents"] != "read" || perms["metadata"] != "read" || perms["statuses"] != "write" || perms["members"] != "read" {
		t.Errorf("default_permissions = %v", perms)
	}
	if ev := m["default_events"].([]any); len(ev) != 1 || ev[0] != "push" {
		t.Errorf("default_events = %v", ev)
	}
}

func TestManifestOverrides(t *testing.T) {
	raw, err := Manifest(ManifestOptions{PublicURL: "https://odyssey.example", WebhookURL: "https://smee.io/abc", Public: true, Description: "d"}, "http://127.0.0.1:1/callback")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Hook   struct{ URL string } `json:"hook_attributes"`
		Public bool                 `json:"public"`
		Desc   string               `json:"description"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Hook.URL != "https://smee.io/abc" || !m.Public || m.Desc != "d" {
		t.Errorf("got %+v", m)
	}
	if _, err := Manifest(ManifestOptions{}, "x"); err == nil {
		t.Error("empty public URL accepted")
	}
}

func TestManifestPostURL(t *testing.T) {
	if got := ManifestPostURL("https://github.com/", ""); got != "https://github.com/settings/apps/new" {
		t.Errorf("personal = %q", got)
	}
	if got := ManifestPostURL("https://github.com", "acme"); got != "https://github.com/organizations/acme/settings/apps/new" {
		t.Errorf("org = %q", got)
	}
}

func TestConvertManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/abc123/conversions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("conversion must be unauthenticated")
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"slug":"odicea","client_id":"Iv1.x","client_secret":"cs","webhook_secret":"ws","pem":"-----BEGIN-----","html_url":"https://github.com/apps/odicea"}`))
	}))
	defer srv.Close()
	c, err := ConvertManifest(context.Background(), srv.Client(), srv.URL, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != 42 || c.Slug != "odicea" || c.ClientID != "Iv1.x" || c.ClientSecret != "cs" || c.WebhookSecret != "ws" || c.PEM != "-----BEGIN-----" || c.HTMLURL != "https://github.com/apps/odicea" {
		t.Errorf("credentials = %#v", c)
	}
	if s := c.String(); strings.Contains(s, "cs") && strings.Contains(s, "ws") || strings.Contains(s, "BEGIN") {
		t.Errorf("String leaks secrets: %s", s)
	}
}

func TestConvertManifestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()
	_, err := ConvertManifest(context.Background(), srv.Client(), srv.URL, "bad")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ConvertManifest(context.Background(), srv.Client(), srv.URL, ""); err == nil {
		t.Error("empty code accepted")
	}
}
