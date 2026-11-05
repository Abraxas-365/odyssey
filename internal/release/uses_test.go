package release_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func baseInput() release.UsesInput {
	s := release.DefaultSettings(identity.NewOrganizationID(), project.NewEnvironmentID())
	return release.UsesInput{Settings: s, Secrets: release.Secrets{Items: []release.SecretView{}, Cluster: release.ClusterSecrets{Key: &release.ClusterKey{KeyID: "k"}}},
		AI:     release.AIState{Status: "off"},
		Egress: release.ClusterEgress{Available: true, Mode: "internet", State: release.EgressApplied, Enforced: true}}
}

func kinds(u release.Uses) []release.AttentionKind {
	out := []release.AttentionKind{}
	for _, a := range u.Attention {
		out = append(out, a.Kind)
	}
	return out
}

func managed(name string, state release.SecretState) release.SecretView {
	return release.SecretView{SecretRef: release.SecretRef{Name: name, Source: release.SecretManaged}, State: state}
}

func external(name string, state release.SecretState) release.SecretView {
	return release.SecretView{SecretRef: release.SecretRef{Name: name, Source: release.SecretExternal}, State: state}
}

// An app with nothing to attend to has an empty (not null) attention list.
func TestUsesNothingPending(t *testing.T) {
	u := release.NewUses(baseInput())
	if u.Attention == nil || len(u.Attention) != 0 || u.Unavailable == nil || len(u.Unavailable) != 0 {
		t.Fatalf("attention %#v unavailable %#v", u.Attention, u.Unavailable)
	}
	if !u.Identity.Verifiable || u.Access.Mode != release.AccessTeam || u.Access.Pending || u.Resources == nil || u.Secrets == nil || u.Secrets.Missing == nil ||
		u.AI == nil || u.Egress == nil {
		t.Fatalf("uses %+v", u)
	}
	if u.NeedsPeople() {
		t.Fatal("nothing pending needs people")
	}
}

// Every Attention kind comes from its own condition, with its who and a
// Spanish message; needs people is any who but wait.
func TestUsesAttention(t *testing.T) {
	applied := int64(1)
	with := func(f func(*release.UsesInput)) release.Uses {
		in := baseInput()
		f(&in)
		return release.NewUses(in)
	}
	use := func(in *release.UsesInput, kind resourcekind.Kind, name string, st release.ResourceStatus) {
		in.Settings.Resources = append(in.Settings.Resources, release.ResourceUse{Name: name, Kind: kind})
		in.Resources = append(in.Resources, release.ResourceState{Name: name, Kind: kind, Status: st, Message: "OOM"})
	}
	type want struct {
		kind release.AttentionKind
		who  release.AttentionWho
	}
	cases := map[string]struct {
		uses release.Uses
		want []want
		text string // a piece of the message
	}{
		"secret_missing": {with(func(in *release.UsesInput) {
			in.Secrets.Items = []release.SecretView{managed("STRIPE_KEY", release.SecretStateMissing)}
		}), []want{{release.AttentionSecretMissing, release.WhoOwner}}, "Falta cargar 1 secreto"},
		"secret to reload (the key changed)": {with(func(in *release.UsesInput) {
			in.Secrets.Items = []release.SecretView{managed("A", release.SecretStateReload), managed("B", release.SecretStateFailed)}
		}), []want{{release.AttentionSecretMissing, release.WhoOwner}}, "Faltan por cargar 2 secretos"},
		"cluster without a key is not 'to load'": {with(func(in *release.UsesInput) {
			in.Secrets.Cluster.Key = nil
			in.Secrets.Items = []release.SecretView{managed("A", release.SecretStateReload), managed("B", release.SecretStateReload)}
		}), []want{{release.AttentionSecretsUnavailable, release.WhoIT}}, "todavía no puede recibir datos privados"},
		"no key, one value loaded before it went away, one never loaded": {with(func(in *release.UsesInput) {
			in.Secrets.Cluster.Key = nil
			in.Secrets.Items = []release.SecretView{managed("A", release.SecretStateReload), managed("B", release.SecretStateMissing)}
		}), []want{{release.AttentionSecretsUnavailable, release.WhoIT}}, "TI debe revisarlo"},
		"vault unreadable": {with(func(in *release.UsesInput) {
			in.Secrets.Items = []release.SecretView{external("V", release.SecretStateUnavailable), external("W", release.SecretStateUnavailable)}
		}), []want{{release.AttentionSecretsUnavailable, release.WhoIT}}, "No pudimos leer 2 secretos de la bóveda"},
		"resource_not_ready": {with(func(in *release.UsesInput) { use(in, resourcekind.Postgres, "db", release.ResourcePreparing) }),
			[]want{{release.AttentionResourceNotReady, release.WhoWait}}, "se está preparando"},
		"resource deleting has its own text": {with(func(in *release.UsesInput) { use(in, resourcekind.Redis, "cache", release.ResourceDeleting) }),
			[]want{{release.AttentionResourceNotReady, release.WhoWait}}, "se está eliminando"},
		"resource_failed": {with(func(in *release.UsesInput) { use(in, resourcekind.Redis, "cache", release.ResourceFailed) }),
			[]want{{release.AttentionResourceFailed, release.WhoIT}}, "No se pudo preparar"},
		"ai revoke_failed": {with(func(in *release.UsesInput) {
			in.AI = release.AIState{Available: true, Status: release.AIRevokeFailed, Warning: true}
		}),
			[]want{{release.AttentionAIError, release.WhoIT}}, "retirar la llave de IA anterior"},
		"ai enabled with the old key not revoked": {with(func(in *release.UsesInput) {
			in.Settings.AI.Enabled = true
			in.AI = release.AIState{Available: true, Status: "enabled", Warning: true}
		}), []want{{release.AttentionAIError, release.WhoIT}}, "no se pudo retirar la anterior"},
		"ai rotating with the old key not revoked": {with(func(in *release.UsesInput) {
			in.Settings.AI.Enabled = true
			in.AI = release.AIState{Available: true, Status: "rotating", Warning: true}
		}), []want{{release.AttentionAIError, release.WhoIT}, {release.AttentionAIPending, release.WhoWait}}, "no se pudo retirar la anterior"},
		"ai on but the gateway is gone": {with(func(in *release.UsesInput) {
			in.Settings.AI.Enabled = true
			in.AI = release.AIState{Available: false, Status: "enabled"}
		}), []want{{release.AttentionAIError, release.WhoIT}}, "ahora no está disponible"},
		"ai enabling": {with(func(in *release.UsesInput) { in.AI = release.AIState{Available: true, Status: "enabling"} }),
			[]want{{release.AttentionAIPending, release.WhoWait}}, "se está activando"},
		"config_pending": {with(func(in *release.UsesInput) {
			in.Serving, in.Applied, in.Active = true, &applied, true
			in.Settings.Revision = 2
		}), []want{{release.AttentionConfigPending, release.WhoWait}}, "Aplicando los últimos cambios"},
		"config_failed": {with(func(in *release.UsesInput) {
			in.Serving, in.Applied, in.Active = true, &applied, false
			in.Settings.Revision = 2
		}), []want{{release.AttentionConfigFailed, release.WhoOwner}}, "Vuelve a guardar"},
		"egress pending waits": {with(func(in *release.UsesInput) {
			in.Egress = release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressPending}
		}), []want{{release.AttentionEgressNotApplied, release.WhoWait}}, "todavía está aplicando"},
		"egress unsupported is for IT": {with(func(in *release.UsesInput) {
			in.Egress = release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressUnsupported}
		}), []want{{release.AttentionEgressNotApplied, release.WhoIT}}, "El clúster no puede aplicar la política de salida"},
		"egress failed is for IT": {with(func(in *release.UsesInput) {
			in.Egress = release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressFailed}
		}), []want{{release.AttentionEgressNotApplied, release.WhoIT}}, "El agente no pudo aplicar la política de salida"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var got []want
			for _, a := range c.uses.Attention {
				got = append(got, want{a.Kind, a.Who})
				if !a.Kind.Valid() || !a.Who.Valid() || a.Message == "" {
					t.Errorf("bad attention %+v", a)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("attention %v, want %v", got, c.want)
			}
			if !slices.ContainsFunc(c.uses.Attention, func(a release.Attention) bool { return strings.Contains(a.Message, c.text) }) {
				t.Errorf("no message with %q: %+v", c.text, c.uses.Attention)
			}
			needs := slices.ContainsFunc(c.want, func(w want) bool { return w.who != release.WhoWait })
			if c.uses.NeedsPeople() != needs {
				t.Errorf("needs people %v, want %v", c.uses.NeedsPeople(), needs)
			}
		})
	}
}

// Nothing to attend to although something looks unusual.
func TestUsesQuietCases(t *testing.T) {
	applied := int64(2)
	in := baseInput()
	in.Settings.Revision = 2
	in.Serving, in.Applied = true, &applied // the app online has the settings in force
	in.Settings.Access.Mode = release.AccessPublic
	in.Settings.Resources = []release.ResourceUse{{Name: "db", Kind: resourcekind.Postgres, Prefix: "MAIN"}}
	in.Resources = []release.ResourceState{{Name: "db", Kind: resourcekind.Postgres, Status: release.ResourceReady}}
	in.Secrets.Items = []release.SecretView{managed("OK", release.SecretStateLoaded), external("VAULT", release.SecretStateExternal)}
	in.Egress = release.ClusterEgress{Available: true, Mode: "internet", State: release.EgressUnsupported} // open internet: nothing to apply
	u := release.NewUses(in)
	if len(u.Attention) != 0 {
		t.Fatalf("attention %+v", u.Attention)
	}
	if u.Identity.Verifiable || u.Secrets.Total != 2 || u.Secrets.External != 1 || len(u.Secrets.Missing) != 0 || u.Secrets.Unavailable != 0 {
		t.Fatalf("uses %+v", u)
	}
	if len(u.Resources) != 1 || u.Resources[0].Variables[0] != "MAIN_DATABASE_URL" || u.Resources[0].Status != release.ResourceReady {
		t.Fatalf("resources %+v", u.Resources)
	}
	// not serving: a stored mode has nothing to reach yet
	in.Serving, in.Applied = false, nil
	in.Settings.Revision = 5
	if u := release.NewUses(in); u.Access.Pending {
		t.Fatalf("pending with nothing online: %+v", u.Access)
	}
	// no cluster at all: no egress to apply
	in.Egress = release.ClusterEgress{}
	if u := release.NewUses(in); len(u.Attention) != 0 || u.Egress == nil || u.Egress.Mode != "" {
		t.Fatalf("no cluster: %+v", u)
	}
	// connected to a resource that no longer exists: failed, not a panic
	in.Resources = nil
	if u := release.NewUses(in); len(u.Attention) != 1 || u.Attention[0].Kind != release.AttentionResourceFailed {
		t.Fatalf("vanished resource: %+v", u.Attention)
	}
}

// A source that could not be asked leaves its section out (and says so)
// instead of guessing: no secret_missing from a cluster that did not answer.
func TestUsesUnavailableSections(t *testing.T) {
	in := baseInput()
	in.Settings.Resources = []release.ResourceUse{{Name: "db", Kind: resourcekind.Postgres}}
	in.Settings.AI.Enabled = true
	in.Secrets.Items = []release.SecretView{managed("A", release.SecretStateMissing)}
	in.Unavailable = []release.UsesSection{release.SectionEgress, release.SectionSecrets, release.SectionAI, release.SectionResources}
	u := release.NewUses(in)
	if u.Secrets != nil || u.AI != nil || u.Egress != nil {
		t.Fatalf("sections not left out: %+v", u)
	}
	if want := []release.UsesSection{release.SectionResources, release.SectionSecrets, release.SectionAI, release.SectionEgress}; !slices.Equal(u.Unavailable, want) {
		t.Fatalf("unavailable %v, want %v", u.Unavailable, want)
	}
	if len(u.Attention) != 0 || u.NeedsPeople() {
		t.Fatalf("an unknown is not an attention: %+v", u.Attention)
	}
	if len(u.Resources) != 1 || u.Resources[0].Status != release.ResourceUnknown || u.Resources[0].Variables == nil {
		t.Fatalf("resources %+v", u.Resources)
	}
	// what the settings hold is still there
	if u.Access.Mode != release.AccessTeam || !u.Identity.Verifiable {
		t.Fatalf("access %+v", u)
	}
	// one section down does not hide the others
	in.Unavailable = []release.UsesSection{release.SectionEgress}
	if u := release.NewUses(in); u.Egress != nil || u.Secrets == nil || u.AI == nil || len(u.Unavailable) != 1 {
		t.Fatalf("only egress is down: %+v", u)
	}
}

// The closed list is complete: every kind and who is valid, the order is the list's.
func TestUsesClosedLists(t *testing.T) {
	if release.AttentionKind("x").Valid() || release.AttentionWho("x").Valid() {
		t.Fatal("unknown values are valid")
	}
	in := baseInput()
	in.Settings.AI.Enabled = true
	in.Secrets.Items = []release.SecretView{managed("A", release.SecretStateMissing)}
	in.Egress = release.ClusterEgress{Available: true, Mode: release.EgressRestricted, State: release.EgressFailed}
	in.AI = release.AIState{Available: true, Status: release.AIRevokeFailed}
	u := release.NewUses(in)
	want := []release.AttentionKind{release.AttentionSecretMissing, release.AttentionAIError, release.AttentionEgressNotApplied}
	if !slices.Equal(kinds(u), want) {
		t.Fatalf("order %v, want %v", kinds(u), want)
	}
}
