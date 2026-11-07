package inventoryhttp_test

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/inventory/adapters/inventoryhttp"
	"github.com/Abraxas-365/odyssey/internal/release"
)

func TestCSVNeutralizesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1": "'=1+1", "+cmd": "'+cmd", "-2": "'-2", "@SUM(A1)": "'@SUM(A1)", "\tx": "'\tx", "\rx": "'\rx",
		"Pagos": "Pagos", "": "", "a=b": "a=b",
	} {
		if got := inventoryhttp.Safe(in); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
	}

	rows := []inventory.Row{{
		App:       inventory.AppRef{Name: `=HYPERLINK("http://evil","click")`, Tier: "internal"},
		Resources: []inventory.ResourceRef{{Name: "db", Kind: "postgres", Status: "ready"}},
		Secrets:   inventory.SecretsSummary{Total: 2, Missing: []string{"@KEY", "OK"}},
		Scan:      inventory.Scan{State: inventory.ScanVulnerable, Critical: 1, High: 2},
		Cluster:   &inventory.ClusterRef{Name: "+Lima, \"norte\""},
		Services:  []release.ServiceRef{{Name: "web", Kind: "web", Route: "/"}, {Name: "=jobs", Kind: "worker"}},
	}}
	out, err := inventoryhttp.CSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "\ufeff") {
		t.Fatal("no BOM")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(out), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, out)
	}
	if len(records) != 2 || len(records[0]) != len(inventoryhttp.Header) || len(records[1]) != len(inventoryhttp.Header) {
		t.Fatalf("shape %d x %d", len(records), len(records[0]))
	}
	for _, rec := range records[1:] {
		for _, cell := range rec {
			if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
				t.Fatalf("cell %q would be read as a formula", cell)
			}
		}
	}
	if records[1][0] != `'=HYPERLINK("http://evil","click")` || records[1][16] != `'+Lima, "norte"` || records[1][12] != "@KEY; OK" && records[1][12] != "'@KEY; OK" {
		t.Fatalf("row %q", records[1])
	}
	if got := records[1][len(inventoryhttp.Header)-1]; got != "web '=jobs" && got != "web =jobs" {
		t.Fatalf("services cell %q", got)
	}
	if inventoryhttp.Header[len(inventoryhttp.Header)-1] != "Servicios" {
		t.Fatalf("Servicios must be the last column: %v", inventoryhttp.Header)
	}
}
