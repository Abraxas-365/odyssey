package project_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Portal de Ventas":        "portal-de-ventas",
		"  Cotizador Ñandú 2.0 ":  "cotizador-nandu-2-0",
		"App__RRHH!!":             "app-rrhh",
		"---":                     "",
		strings.Repeat("ab ", 30): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-a", // cut to 40
	} {
		if got := project.Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for s, want := range map[string]bool{
		"portal": true, "a1-b2": true, "abc": true,
		"ab": false, "1abc": false, "abc-": false, "a--b": false, "Abc": false, "a_b": false,
		strings.Repeat("a", 41): false, strings.Repeat("a", 40): true,
	} {
		if got := project.ValidSlug(s); got != want {
			t.Errorf("ValidSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestCreateValidate(t *testing.T) {
	user := identity.MustParseUserID("6f1c2a52-8f0e-4a8e-9d2c-0b7d1f3e4a55")
	ok := project.Create{Name: "Portal de ventas", CreatedBy: user}.Normalize()
	if ok.Slug != "portal-de-ventas" {
		t.Fatalf("derived slug %q", ok.Slug)
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]project.Create{
		"no name":    {Slug: "abc", CreatedBy: user},
		"bad slug":   {Name: "x", Slug: "A B", CreatedBy: user},
		"no creator": {Name: "Portal", Slug: "portal"},
	} {
		if c.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestUpdateChanges(t *testing.T) {
	name, desc := " Portal ", "nuevo"
	u := project.Update{Name: &name, Description: &desc}.Changes(project.Project{Name: "Portal", Description: "viejo"})
	if u.Name != nil || u.Description == nil || u.Empty() {
		t.Fatalf("changes %+v", u)
	}
}
