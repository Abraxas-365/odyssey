package resourcespec_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

func TestRequires(t *testing.T) {
	if got := (resourcespec.Spec{}).Requires(resourcekind.Redis); !slices.Equal(got, []string{"builtin:redis"}) {
		t.Fatalf("basic: %v", got)
	}
	// an empty postgres block is the basic database too
	if got := (resourcespec.Spec{Postgres: &resourcespec.Postgres{}}).Requires(resourcekind.Postgres); !slices.Equal(got, []string{"builtin:postgres"}) {
		t.Fatalf("empty block: %v", got)
	}
	s := resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2, Memory: "1Gi"}}
	want := []string{"builtin:postgres", resourcespec.PostgresInstances, resourcespec.PostgresResources, resourcespec.SpecV1}
	slices.Sort(want)
	if got := s.Requires(resourcekind.Postgres); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if m := resourcespec.Missing(want, []string{"builtin:postgres", resourcespec.SpecV1}); len(m) != 2 {
		t.Fatalf("missing %v", m)
	}
}

func TestValidate(t *testing.T) {
	ok := []resourcespec.Spec{
		{},
		{Postgres: &resourcespec.Postgres{Instances: 2, Major: 17, CPU: "500m", Memory: "1.5Gi"}},
		{Postgres: &resourcespec.Postgres{CPU: "1"}},
	}
	for _, s := range ok {
		if err := s.Validate(resourcekind.Postgres); err != nil {
			t.Errorf("%+v: %v", s.Postgres, err)
		}
	}
	bad := []resourcespec.Postgres{
		{Instances: -1}, {Instances: resourcespec.MaxInstances + 1}, {Major: 12},
		{CPU: "lots"}, {Memory: "1GB"}, {CPU: "m"}, {Memory: "12345678901234567Gi"},
		{CPU: "."}, {CPU: ".m"}, {Memory: "1.5.2Gi"},
	}
	for _, p := range bad {
		if err := (resourcespec.Spec{Postgres: &p}).Validate(resourcekind.Postgres); err == nil {
			t.Errorf("%+v passed", p)
		}
	}
	if err := (resourcespec.Spec{Postgres: &resourcespec.Postgres{Instances: 2}}).Validate(resourcekind.Redis); err == nil {
		t.Error("redis took postgres settings")
	}
}

// A resource the company already has requires only the existing engine and
// the stores it reads, never the kind's built-in engine.
func TestRequiresExisting(t *testing.T) {
	s := resourcespec.ExamplePostgres("vault")
	s.Existing.Values[3].Ref.Store = "aws-prod"
	want := []string{resourcespec.ConnectExisting, "secretstore:aws-prod", "secretstore:vault"}
	if got := s.Requires(resourcekind.Postgres); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if s.IsZero() || s.Engine() != resourcespec.EngineExisting || (resourcespec.Spec{}).Engine() != resourcespec.EngineBuiltin {
		t.Fatal("engine")
	}
	if got := s.Existing.Stores(); !slices.Equal(got, []string{"aws-prod", "vault"}) {
		t.Fatalf("stores %v", got)
	}
}

func TestValidateExisting(t *testing.T) {
	if err := resourcespec.ExamplePostgres("vault").Validate(resourcekind.Postgres); err != nil {
		t.Fatal(err)
	}
	ref := &resourcespec.Ref{Store: "vault", Key: "s3/reportes", Property: "key"}
	s3 := resourcespec.Spec{Existing: &resourcespec.Existing{Values: []resourcespec.Value{
		{Variable: "S3_BUCKET", Value: "reportes"}, {Variable: "S3_REGION", Value: "us-east-1"},
	}}}
	if err := s3.Validate(resourcekind.ObjectStorage); err != nil {
		t.Fatalf("optional variables left out: %v", err)
	}
	s3.Existing.Values = append(s3.Existing.Values, resourcespec.Value{Variable: "S3_ENDPOINT", Value: "https://minio.empresa.local:9000"},
		resourcespec.Value{Variable: "AWS_ACCESS_KEY_ID", Ref: ref})
	if err := s3.Validate(resourcekind.ObjectStorage); err == nil {
		t.Fatal("a key without its secret accepted")
	}
	s3.Existing.Values = append(s3.Existing.Values, resourcespec.Value{Variable: "AWS_SECRET_ACCESS_KEY", Ref: ref})
	if err := s3.Validate(resourcekind.ObjectStorage); err != nil {
		t.Fatalf("with endpoint and key: %v", err)
	}
	// a company's database usually has a private address or an internal name
	for _, h := range []string{"10.0.4.12", "reportes.db.empresa.internal", "db-1.corp", "2001:db8::1"} {
		s := resourcespec.ExamplePostgres("vault")
		for i := range s.Existing.Values {
			if s.Existing.Values[i].Variable == "PGHOST" {
				s.Existing.Values[i].Value = h
			}
		}
		if err := s.Validate(resourcekind.Postgres); err != nil {
			t.Errorf("host %s: %v", h, err)
		}
	}
	set := func(variable string, v resourcespec.Value) func(*resourcespec.Existing) {
		return func(e *resourcespec.Existing) {
			for i := range e.Values {
				if e.Values[i].Variable == variable {
					v.Variable = variable
					e.Values[i] = v
				}
			}
		}
	}
	add := func(v resourcespec.Value) func(*resourcespec.Existing) {
		return func(e *resourcespec.Existing) { e.Values = append(e.Values, v) }
	}
	for name, change := range map[string]func(*resourcespec.Existing){
		"password written":   set("PGPASSWORD", resourcespec.Value{Value: "hunter2"}),
		"value and ref":      set("PGUSER", resourcespec.Value{Value: "app", Ref: ref}),
		"neither":            set("PGUSER", resourcespec.Value{}),
		"missing":            func(e *resourcespec.Existing) { e.Values = e.Values[1:] },
		"twice":              func(e *resourcespec.Existing) { e.Values = append(e.Values, e.Values[0]) },
		"derived":            add(resourcespec.Value{Variable: "DATABASE_URL", Ref: ref}),
		"unknown variable":   add(resourcespec.Value{Variable: "PGSERVICE", Value: "x"}),
		"port text":          set("PGPORT", resourcespec.Value{Value: "cinco"}),
		"port 0":             set("PGPORT", resourcespec.Value{Value: "0"}),
		"port padded":        set("PGPORT", resourcespec.Value{Value: "05432"}),
		"host with path":     set("PGHOST", resourcespec.Value{Value: "db.local/x"}),
		"metadata service":   set("PGHOST", resourcespec.Value{Value: "169.254.169.254"}),
		"cluster service":    set("PGHOST", resourcespec.Value{Value: "odyssey-api.odyssey.svc"}),
		"cluster fqdn":       set("PGHOST", resourcespec.Value{Value: "db.default.svc.cluster.local"}),
		"localhost":          set("PGHOST", resourcespec.Value{Value: "localhost"}),
		"loopback":           set("PGHOST", resourcespec.Value{Value: "127.0.0.1"}),
		"loopback v6":        set("PGHOST", resourcespec.Value{Value: "::1"}),
		"loopback as number": set("PGHOST", resourcespec.Value{Value: "2130706433"}),
		"loopback as hex":    set("PGHOST", resourcespec.Value{Value: "0x7f000001"}),
		"dotted number":      set("PGHOST", resourcespec.Value{Value: "127.1"}),
		"one label":          set("PGHOST", resourcespec.Value{Value: "postgres"}),
		"under localhost":    set("PGHOST", resourcespec.Value{Value: "db.localhost"}),
		"gcp metadata":       set("PGHOST", resourcespec.Value{Value: "metadata.google.internal"}),
		"host template":      set("PGHOST", resourcespec.Value{Value: "{{ .password }}"}),
		"spaces around":      set("PGDATABASE", resourcespec.Value{Value: " reportes"}),
		"control character":  set("PGDATABASE", resourcespec.Value{Value: "rep\nortes"}),
		"long value":         set("PGDATABASE", resourcespec.Value{Value: strings.Repeat("a", resourcespec.MaxValue+1)}),
		"store uppercase":    set("PGUSER", resourcespec.Value{Ref: &resourcespec.Ref{Store: "Vault", Key: "k"}}),
		"store empty":        set("PGUSER", resourcespec.Value{Ref: &resourcespec.Ref{Key: "k"}}),
		"key with space":     set("PGUSER", resourcespec.Value{Ref: &resourcespec.Ref{Store: "vault", Key: "rds reportes"}}),
		"key with braces":    set("PGUSER", resourcespec.Value{Ref: &resourcespec.Ref{Store: "vault", Key: "{{x}}"}}),
		"property with path": set("PGUSER", resourcespec.Value{Ref: &resourcespec.Ref{Store: "vault", Key: "k", Property: "a/b"}}),
		"sslmode not a mode": add(resourcespec.Value{Variable: "PGSSLMODE", Value: "on"}),
		"sslmode from store": add(resourcespec.Value{Variable: "PGSSLMODE", Ref: ref}),
	} {
		s := resourcespec.ExamplePostgres("vault")
		change(s.Existing)
		if err := s.Validate(resourcekind.Postgres); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, endpoint := range map[string]string{
		"no scheme": "minio.local", "ftp": "ftp://minio.local", "user": "https://a:b@minio.local",
		"query": "https://minio.local?x=1", "bad port": "https://minio.local:99999",
	} {
		s := resourcespec.Spec{Existing: &resourcespec.Existing{Values: []resourcespec.Value{
			{Variable: "S3_BUCKET", Value: "r"}, {Variable: "S3_REGION", Value: "us-east-1"}, {Variable: "S3_ENDPOINT", Value: endpoint},
		}}}
		if err := s.Validate(resourcekind.ObjectStorage); err == nil {
			t.Errorf("endpoint %s: accepted", name)
		}
	}
	// files are mounted (never the company's), and engine settings do not mix
	if err := (resourcespec.Spec{Existing: &resourcespec.Existing{Values: []resourcespec.Value{{Variable: "FILES_DIR", Value: "/data"}}}}).Validate(resourcekind.Files); err == nil {
		t.Error("files accepted an existing connection")
	}
	mixed := resourcespec.ExamplePostgres("vault")
	mixed.Postgres = &resourcespec.Postgres{Instances: 2}
	if err := mixed.Validate(resourcekind.Postgres); err == nil {
		t.Error("existing with postgres settings accepted")
	}
	if err := resourcespec.ExamplePostgres("vault").Validate(resourcekind.Redis); err == nil {
		t.Error("postgres variables accepted for redis")
	}
}
