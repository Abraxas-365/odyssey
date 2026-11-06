package fleet_test

import (
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/event/eventtest"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

var (
	org     = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	user    = identity.MustParseUserID("00000000-0000-4000-8000-000000000002")
	cluster = fleet.MustParseClusterID("00000000-0000-4000-8000-0000000000c1")
	env     = project.MustParseEnvironmentID("00000000-0000-4000-8000-0000000000bb")
	proj    = project.MustParseID("00000000-0000-4000-8000-0000000000aa")
	at      = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
)

// TestEventContracts pins each payload's JSON (testdata/events).
func TestEventContracts(t *testing.T) {
	expires := at.Add(fleet.TokenTTL)
	c := fleet.Cluster{ID: cluster, Organization: org, Name: "Producción Lima", TokenExpiresAt: &expires}
	cert := fleet.Issued{Serial: "5a1f", ExpiresAt: at.Add(fleet.CertificateTTL)}
	app := fleet.App{Organization: org, Project: proj, Environment: env, Cluster: cluster, Name: "visitas",
		Revision: 3, Image: "registry.local/visitas@sha256:" + strings.Repeat("a", 64),
		Env: map[string]string{"B_KEY": "secret", "A_KEY": "secret"}}
	for _, e := range []event.Event{
		fleet.NewClusterCreated(org, user, c),
		fleet.NewTokenIssued(org, user, c, fleet.Token{ExpiresAt: expires}),
		fleet.NewEnrolled(org, c, cert),
		fleet.NewCertificateRenewed(org, c, cert),
		fleet.NewConnected(org, c, fleet.Agent{Version: "0.3.0",
			Capabilities: fleet.Capabilities{KubernetesVersion: "v1.31.4+k3s1"}}, "203.0.113.7"),
		fleet.NewRevoked(org, user, c),
		fleet.NewAppsMoved(org, user, c, fleet.Cluster{ID: fleet.MustParseClusterID("00000000-0000-4000-8000-0000000000c2"), Name: "Producción Arequipa"},
			fleet.Moved{Environments: []project.EnvironmentID{env}, Apps: 2}),
		fleet.NewAppApplied(org, c, app, true),
		fleet.NewAppRetired(org, c, fleet.Retirement{Organization: org, Project: proj, Environment: env, Name: "visitas", Revision: 4}),
		fleet.NewAppRemoved(org, c, fleet.App{Organization: org, Project: proj, Environment: env, Name: "visitas", Revision: 4, Deleted: true}),
		fleet.NewBuildSent(org, c, fleet.DesiredBuild{ID: fleet.MustParseBuildID("00000000-0000-4000-8000-0000000000b1"),
			Project: proj, Environment: env, SourceSHA256: strings.Repeat("b", 64),
			ImageRepository: "registry.local/" + org.String() + "/" + proj.String()}, false),
		fleet.NewBuildReleased(org, fleet.DesiredBuild{ID: fleet.MustParseBuildID("00000000-0000-4000-8000-0000000000b1"),
			Cluster: cluster, Final: true}),
		fleet.NewSecretSent(org, user, c, secretDelivery, true),
		fleet.NewSecretAcked(org, secretDelivery, ""),
		fleet.NewSecretAcked(org, secretDelivery, "the value could not be opened with this cluster's key"),
		fleet.NewResourceApplied(org, c, fleet.DesiredResource{Project: proj, Environment: env, Name: "db", Kind: resourcekind.Postgres,
			Generation: 2, StorageGi: 5}, false),
		fleet.NewResourceRemoved(org, c, fleet.DesiredResource{Project: proj, Environment: env, Name: "db", Kind: resourcekind.Postgres}),
		fleet.NewEgressChanged(org, user, c, fleet.EgressSetting{Egress: fleet.DefaultEgress(), Generation: 1},
			fleet.EgressSetting{Egress: fleet.Egress{Mode: fleet.EgressRestricted, Allow: []string{"203.0.113.0/24"}}, Generation: 2}),
	} {
		eventtest.Golden(t, e)
	}
}

var secretDelivery = fleet.SecretDelivery{ID: mustDelivery("00000000-0000-4000-8000-0000000000f1"),
	Organization: org, Project: proj, Environment: env, Cluster: cluster, App: "visitas", Name: "STRIPE_API_KEY", Version: 2,
	KeyID: "k3yIdK3yIdK3yId0", Sealed: []byte("never in the trail"), CreatedBy: user}

func TestSecretEventsNeverCarryTheSealedValue(t *testing.T) {
	for _, e := range []event.Event{fleet.NewSecretSent(org, user, fleet.Cluster{ID: cluster}, secretDelivery, false),
		fleet.NewSecretAcked(org, secretDelivery, ""), fleet.NewSecretAcked(org, secretDelivery, "x")} {
		if strings.Contains(string(e.Payload), "never in the trail") || strings.Contains(string(e.Payload), "bmV2ZXIg") {
			t.Fatalf("%s carries the sealed value: %s", e.Type, e.Payload)
		}
	}
}

func TestEnvValuesStayOutOfTheTrail(t *testing.T) {
	e := fleet.NewAppApplied(org, fleet.Cluster{ID: cluster}, fleet.App{Name: "a", Revision: 1,
		Env: map[string]string{"API_KEY": "s3cr3t"}}, false)
	if strings.Contains(string(e.Payload), "s3cr3t") || !strings.Contains(string(e.Payload), "API_KEY") {
		t.Fatalf("payload %s", e.Payload)
	}
}

func TestTokenCarriesItsClusterAndOnlyItsHashIsKept(t *testing.T) {
	value, hash := fleet.NewToken(org, cluster)
	if !strings.HasPrefix(value, "odyssey-enroll-") {
		t.Fatalf("token %q has no recognisable prefix", value)
	}
	if string(fleet.HashToken(value)) != string(hash) || strings.Contains(value, string(hash)) {
		t.Fatal("hash does not match the token")
	}
	gotOrg, gotCluster, err := fleet.ParseToken("  " + value + "\n")
	if err != nil || gotOrg != org || gotCluster != cluster {
		t.Fatalf("ParseToken = %v %v %v", gotOrg, gotCluster, err)
	}
	other, _ := fleet.NewToken(org, cluster)
	if other == value {
		t.Fatal("two tokens are equal")
	}
}

func TestMalformedTokensAreOneError(t *testing.T) {
	value, _ := fleet.NewToken(org, cluster)
	for _, bad := range []string{"", "odyssey-enroll-", "nope-" + value[15:], value[:40], value + "AA",
		"odyssey-enroll-" + strings.Repeat("A", 86)} {
		_, _, err := fleet.ParseToken(bad)
		var e *errx.Error
		if !errx.As(err, &e) || e.Code != fleet.CodeInvalidToken {
			t.Errorf("ParseToken(%q) = %v, want %s", bad, err, fleet.CodeInvalidToken)
		}
	}
}

func TestAgentURIRoundTrips(t *testing.T) {
	u, err := url.Parse(fleet.AgentURI(org, cluster))
	if err != nil {
		t.Fatal(err)
	}
	o, c, err := fleet.ParseAgentURI(u)
	if err != nil || o != org || c != cluster {
		t.Fatalf("ParseAgentURI = %v %v %v", o, c, err)
	}
	for _, bad := range []string{
		"spiffe://evil.dev/org/" + org.String() + "/cluster/" + cluster.String(),
		"spiffe://odyssey.dev/org/" + org.String(),
		"spiffe://odyssey.dev/org/x/cluster/" + cluster.String(),
		"spiffe://odyssey.dev/org/" + org.String() + "/cluster/" + cluster.String() + "/extra",
	} {
		u, _ := url.Parse(bad)
		if _, _, err := fleet.ParseAgentURI(u); err == nil {
			t.Errorf("ParseAgentURI(%q) accepted", bad)
		}
	}
}

func TestReportsAreBounded(t *testing.T) {
	ok := fleet.AppStatus{Environment: env, Name: "visitas", ObservedRevision: 2, Phase: fleet.PhaseReady, URL: "https://visitas.apps.example"}
	if err := (fleet.Report{Apps: []fleet.AppStatus{{Environment: env, Name: "a", Phase: fleet.PhaseFailed, Reason: fleet.ReasonResource}}}).Validate(); err != nil {
		t.Fatalf("the resource reason was rejected: %v", err)
	}
	if err := (fleet.Report{Apps: []fleet.AppStatus{ok}}).Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	for name, s := range map[string]fleet.AppStatus{
		"no environment":  {Name: "a", Phase: fleet.PhaseReady},
		"bad name":        {Environment: env, Name: "A_b", Phase: fleet.PhaseReady},
		"bad phase":       {Environment: env, Name: "a", Phase: "exploded"},
		"bad reason":      {Environment: env, Name: "a", Phase: fleet.PhaseFailed, Reason: "cosmic_ray"},
		"negative":        {Environment: env, Name: "a", Phase: fleet.PhaseReady, ObservedRevision: -1},
		"huge message":    {Environment: env, Name: "a", Phase: fleet.PhaseReady, Message: strings.Repeat("x", fleet.MaxMessage+1)},
		"control in text": {Environment: env, Name: "a", Phase: fleet.PhaseReady, Message: "a\x1b[2Jb"},
		"javascript url":  {Environment: env, Name: "a", Phase: fleet.PhaseReady, URL: "javascript:alert(document.cookie)"},
		"data url":        {Environment: env, Name: "a", Phase: fleet.PhaseReady, URL: "data:text/html,<script>x</script>"},
		"relative url":    {Environment: env, Name: "a", Phase: fleet.PhaseReady, URL: "//evil.example/x"},
		"url with creds":  {Environment: env, Name: "a", Phase: fleet.PhaseReady, URL: "https://u:p@evil.example"},
	} {
		if _, err := (fleet.Report{Apps: []fleet.AppStatus{s}}).Valid(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	kept, err := (fleet.Report{Apps: []fleet.AppStatus{ok, {Name: "bad"}, ok}}).Valid()
	if err == nil || len(kept.Apps) != 2 {
		t.Errorf("a partly invalid report kept %d statuses (%v)", len(kept.Apps), err)
	}
	many := make([]fleet.AppStatus, fleet.MaxReportApps+1)
	for i := range many {
		many[i] = ok
	}
	if err := (fleet.Report{Apps: many}).Validate(); err == nil {
		t.Error("an oversized report was accepted")
	}
	if err := (fleet.Agent{Capabilities: fleet.Capabilities{APIs: make([]string, fleet.MaxReported+1)}}).Validate(); err == nil {
		t.Error("too many capabilities accepted")
	}
}

// What an agent says of a build is made safe to show, never trusted as is.
func TestBuildReportsAreCleaned(t *testing.T) {
	id := fleet.MustParseBuildID("00000000-0000-4000-8000-0000000000b1")
	s := fleet.BuildStatus{Build: id, Phase: fleet.BuildRunning, Reason: "push", Message: "\x1b]0;title\x07ok\x00"}.Clean()
	if s.Reason != "" || s.Message != "ok" {
		t.Errorf("running status cleaned to %+v", s)
	}
	if s := (fleet.BuildStatus{Build: id, Phase: fleet.BuildFailed, Reason: "timeout"}).Clean(); s.Reason != "timeout" {
		t.Errorf("a known reason was replaced: %+v", s)
	}
	for _, bad := range []fleet.BuildStatus{
		{Phase: fleet.BuildRunning},
		{Build: id, Phase: "exploded"},
		{Build: id, Phase: fleet.BuildSucceeded, Digest: "latest"},
		{Build: id, Phase: fleet.BuildFailed, Digest: "sha256:" + strings.Repeat("a", 64)},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	long, ok := fleet.LogLine{Seq: 1, Stream: "build", Text: strings.Repeat("ñ", fleet.MaxLogLineBytes)}.Clean()
	if !ok || len(long.Text) > fleet.MaxLogLineBytes || !strings.HasSuffix(long.Text, "…") {
		t.Errorf("a long line kept %d bytes", len(long.Text))
	}
	for _, l := range []fleet.LogLine{{Seq: 0, Stream: "build"}, {Seq: fleet.MaxLogSeq + 1, Stream: "build"}, {Seq: 1, Stream: "stdout"}} {
		if _, ok := l.Clean(); ok {
			t.Errorf("%+v kept", l)
		}
	}
	b := fleet.DesiredBuild{ID: id, Organization: org, Project: proj, Environment: env, SourceSHA256: strings.Repeat("a", 64), SourceSize: 1, Timeout: time.Minute}
	stranger := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000009")
	for key, valid := range map[string]bool{
		blob.TenantKey(org, "sources", "x.tar.gz"):                     true,
		blob.TenantKey(stranger, "sources", "x.tar.gz"):                false,
		blob.TenantKey(org, "builds", "x.log.jsonl"):                   false,
		blob.TenantKey(org, "sources", "..", "..", "sources", "x.tar"): false,
		"": false,
	} {
		b.SourceKey = key
		if (b.Validate() == nil) != valid {
			t.Errorf("source key %q: valid=%v", key, !valid)
		}
	}
}

func TestAppAccessModes(t *testing.T) {
	base := fleet.App{Organization: org, Project: proj, Environment: env, Cluster: cluster, Name: "web", Revision: 1, Image: "r/web@sha256:00"}
	for mode, valid := range map[string]bool{"": true, "public": true, "team": true, "organization": true, "everyone": false, "Team": false} {
		a := base
		a.Access = mode
		if (a.Validate() == nil) != valid {
			t.Errorf("access %q: valid=%v, want %v", mode, !valid, valid)
		}
	}
}

// What an agent says of a scan is made safe to show; the database date is kept
// (normalized to UTC) when it reads and dropped when it does not.
func TestScanReportsAreCleaned(t *testing.T) {
	id := fleet.MustParseBuildID("00000000-0000-4000-8000-0000000000b1")
	s := fleet.ScanStatus{Build: id, Digest: "sha256:" + strings.Repeat("a", 64), Tool: "trivy\x00 0.76.2", High: 3,
		DBUpdatedAt: "2026-11-05T08:09:41-05:00"}.Clean()
	if s.Tool != "trivy 0.76.2" || s.DBUpdatedAt != "2026-11-05T13:09:41Z" || s.High != 3 {
		t.Errorf("cleaned to %+v", s)
	}
	if s := (fleet.ScanStatus{DBUpdatedAt: "yesterday"}).Clean(); s.DBUpdatedAt != "" {
		t.Errorf("an unreadable date was kept: %q", s.DBUpdatedAt)
	}
	if s := (fleet.ScanStatus{Error: "boom", Critical: 4}).Clean(); s.Critical != 0 {
		t.Errorf("the counts of a failed scan were kept: %+v", s)
	}
}

func mustDelivery(raw string) fleet.SecretDeliveryID {
	id, err := fleet.ParseSecretDeliveryID(raw)
	if err != nil {
		panic(err)
	}
	return id
}
