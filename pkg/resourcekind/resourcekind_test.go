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
}
