package resource

import (
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func TestConnectionKeysLineUpWithTheSharedVariables(t *testing.T) {
	for _, k := range []resourcekind.Kind{resourcekind.Postgres, resourcekind.Redis} {
		secret, keys := Connection(k, "db")
		if secret == "" || len(keys) != len(resourcekind.Variables(k, "")) {
			t.Errorf("%s: %q %v vs %v", k, secret, keys, resourcekind.Variables(k, ""))
		}
	}
	if s, keys := Connection(resourcekind.Files, "x"); s != "" || keys != nil {
		t.Errorf("files has no Secret: %q %v", s, keys)
	}
	if s, _ := Connection(resourcekind.Postgres, "main"); s != "main-app" {
		t.Errorf("CNPG's Secret: %q", s)
	}
}
