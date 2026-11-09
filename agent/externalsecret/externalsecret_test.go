package externalsecret

import (
	"net/url"
	"strings"
	"testing"
	"text/template"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// run executes tpl as ESO's engine v2 does, with the functions the agent
// uses (sprig's replace; urlquery is Go's).
func run(t *testing.T, tpl string, data map[string]string) string {
	t.Helper()
	tm, err := template.New("t").Funcs(template.FuncMap{
		"replace": func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
	}).Parse(tpl)
	if err != nil {
		t.Fatalf("%q: %v", tpl, err)
	}
	var b strings.Builder
	if err := tm.Execute(&b, data); err != nil {
		t.Fatalf("%q: %v", tpl, err)
	}
	return b.String()
}

func TestALiteralIsTextNeverATemplate(t *testing.T) {
	for _, s := range []string{"plain", `{{ .v0 }}`, `we"ird\name` + "\n", "}}{{", ""} {
		if got := run(t, Action(Literal(s)), map[string]string{"v0": "SECRET"}); got != s {
			t.Errorf("%q came out as %q", s, got)
		}
	}
}

func TestEscapedReadsBackFromAURL(t *testing.T) {
	pw := `p@ss w/rd%+:?#{{x}}`
	u := "postgresql://" + Action(Escaped(".v0")) + ":" + Action(Escaped(".v1")) + "@h:5432/" + Action(Escaped(Literal("d b")))
	got := run(t, u, map[string]string{"v0": "app@inedge", "v1": pw})
	p, err := url.Parse(got)
	if err != nil {
		t.Fatalf("%q: %v", got, err)
	}
	if back, _ := p.User.Password(); p.User.Username() != "app@inedge" || back != pw || p.Path != "/d b" {
		t.Errorf("%q reads back as %q %q %q", got, p.User.Username(), back, p.Path)
	}
	if strings.Contains(got, "+") {
		t.Errorf("a space must not become +: %q", got)
	}
}

func es(gen int64, ready, synced string) *unstructured.Unstructured {
	e := New()
	e.SetGeneration(gen)
	if ready != "" {
		e.Object["status"] = map[string]any{
			"conditions":            []any{map[string]any{"type": "Ready", "status": ready}},
			"syncedResourceVersion": synced,
		}
	}
	return e
}

func TestStateCountsOnlyTheCurrentGeneration(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	fresh, old := es(3, "False", "2-abc"), es(3, "False", "2-abc")
	Written(fresh, now.Add(-Grace/2))
	Written(old, now.Add(-Grace))
	for name, c := range map[string]struct {
		e    *unstructured.Unstructured
		want State
	}{
		"no status":          {es(2, "", ""), Pending},
		"synced now":         {es(2, "True", "2-abc"), Synced},
		"synced before edit": {es(3, "True", "2-abc"), Pending},
		"generation 12 ≠ 1":  {es(1, "True", "12-abc"), Pending},
		// a refresh failed after this spec synced: the Secret keeps its values
		"stale": {es(2, "False", "2-abc"), Stale},
		// a failure maybe about the previous spec: wait for ESO's verdict
		"failed just after writing": {fresh, Pending},
		"failed":                    {old, Failed},
		"failed, never stamped":     {es(3, "False", "2-abc"), Failed},
	} {
		if got := StateOf(c.e, now); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestSameFollowsTheSpecTheAgentWrote(t *testing.T) {
	a, b := New(), New()
	a.Object["spec"] = map[string]any{"refreshInterval": "1h"}
	b.Object["spec"] = map[string]any{"refreshInterval": "1h"}
	Stamp(a)
	Stamp(b)
	if !Same(a, b) {
		t.Error("the same spec")
	}
	// ESO filling in defaults does not count: the stamp is what was written.
	a.Object["spec"].(map[string]any)["target"] = map[string]any{"deletionPolicy": "Retain"}
	if !Same(a, b) {
		t.Error("defaults ESO added")
	}
	b.Object["spec"] = map[string]any{"refreshInterval": "2h"}
	Stamp(b)
	if Same(a, b) {
		t.Error("another spec")
	}
	if Same(New(), b) {
		t.Error("an object without a stamp is never the same")
	}
}

func TestServed(t *testing.T) {
	m := meta.NewDefaultRESTMapper(nil)
	if Served(m) {
		t.Error("not installed")
	}
	m.Add(GVK, meta.RESTScopeNamespace)
	if !Served(m) {
		t.Error("installed")
	}
	if NewList().GetKind() != "ExternalSecretList" || StoreRef("vault")["kind"] != "ClusterSecretStore" {
		t.Error("kinds")
	}
}
