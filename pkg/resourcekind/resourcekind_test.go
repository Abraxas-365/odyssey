package resourcekind

import (
	"slices"
	"testing"
)

func TestCheckName(t *testing.T) {
	for name, ok := range map[string]bool{
		"db": true, "main-db": true, "a1": true,
		"": false, "1db": false, "Db": false, "db-": false, "db_x": false,
		"a234567890123456789012345678901": false, // 31
	} {
		if got := CheckName(name) == nil; got != ok {
			t.Errorf("CheckName(%q) ok=%v want %v", name, got, ok)
		}
	}
}

func TestCheckPrefix(t *testing.T) {
	for p, ok := range map[string]bool{"": true, "ANALYTICS": true, "CACHE_2": true, "analytics": false, "X_": false, "_X": false, "ODICEA_DB": false} {
		if got := CheckPrefix(p) == nil; got != ok {
			t.Errorf("CheckPrefix(%q) ok=%v want %v", p, got, ok)
		}
	}
}

func TestStorage(t *testing.T) {
	if n, err := ParseStorage(Storage(5)); err != nil || n != 5 {
		t.Fatalf("round trip: %d %v", n, err)
	}
	for _, s := range []string{"0Gi", "101Gi", "5G", "5", "x", "1.5Gi"} {
		if _, err := ParseStorage(s); err == nil {
			t.Errorf("ParseStorage(%q) accepted", s)
		}
	}
}

func TestVariables(t *testing.T) {
	if got := Variables(Postgres, ""); !slices.Contains(got, "DATABASE_URL") || len(got) != 6 {
		t.Errorf("postgres %v", got)
	}
	if got := Variables(Redis, "CACHE"); !slices.Equal(got, []string{"CACHE_REDIS_URL"}) {
		t.Errorf("redis %v", got)
	}
	if got := Variables(Files, ""); !slices.Equal(got, []string{"FILES_DIR"}) {
		t.Errorf("files %v", got)
	}
	for _, k := range Kinds {
		if !k.Valid() {
			t.Errorf("%s invalid", k)
		}
	}
	if Kind("mysql").Valid() {
		t.Error("mysql valid")
	}
	if ObjectStorage.Valid() {
		t.Error("object-storage is declared, not available")
	}
	if KindsLabel() != "postgres, redis or files" {
		t.Errorf("label %q", KindsLabel())
	}
}

// Every contract is complete and consistent: the registry is the one place a
// kind is described, so a gap here is a gap everywhere.
func TestContracts(t *testing.T) {
	seen := map[Kind]bool{}
	for _, c := range Contracts() {
		if seen[c.Kind] || CheckName(string(c.Kind)) != nil {
			t.Errorf("%q: repeated or not a DNS label", c.Kind)
		}
		seen[c.Kind] = true
		if c.Noun == "" || (c.Article != "la" && c.Article != "el") || c.Unavailable == "" || len(c.Variables) == 0 {
			t.Errorf("%s: incomplete contract %+v", c.Kind, c)
		}
		names := map[string]bool{}
		for _, v := range c.Variables {
			if CheckPrefix(v.Name) != nil || names[v.Name] || v.Meaning == "" {
				t.Errorf("%s: variable %q is not an uppercase, unique, explained name", c.Kind, v.Name)
			}
			names[v.Name] = true
		}
		if c.Mounted && c.Variables[0].Secret {
			t.Errorf("%s: a mounted kind's first variable is its path, not a secret", c.Kind)
		}
	}
	if got := Of("nope"); got.The() != "el recurso" || got.Unavailable == "" {
		t.Errorf("unknown kind %+v", got)
	}
	if Of(Files).The() != "el espacio de archivos" || !Of(Files).Mounted || Of(Redis).HoldsData {
		t.Error("files/redis contract")
	}
}
