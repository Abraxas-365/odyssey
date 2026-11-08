package release

import (
	"slices"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Settings of some services only (MS-4). A variable, secret, resource or the
// AI reaches the services its Scope names; an empty Scope reaches the whole
// app, also the services added later. A scope naming a service the version
// no longer has is kept as is: that setting reaches nobody until the service
// comes back (the agent filters by workload), and it never widens to "every
// service" (that would hand a secret to services nobody chose).

// Scope is the services a setting reaches, sorted and without repeats; empty
// is the whole app.
type Scope []string

// CodeServiceSettingsUnavailable: the environment's cluster cannot give a
// setting to some services only (its agent predates MS-4). Same code as the
// fleet's.
const CodeServiceSettingsUnavailable = "SERVICE_SETTINGS_UNAVAILABLE"

// ErrServiceSettingsUnavailable is the 409 for a scope the cluster cannot honor.
func ErrServiceSettingsUnavailable() error {
	return errx.Conflict("El clúster de esta app todavía no puede dar una configuración solo a algunos servicios. Pide a TI que actualice su agente.").
		WithCode(CodeServiceSettingsUnavailable)
}

// Normalize sorts s and drops repeats; the whole app is nil.
func (s Scope) Normalize() Scope {
	if len(s) == 0 {
		return nil
	}
	out := slices.Clone(s)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Whole says s reaches the whole app.
func (s Scope) Whole() bool { return len(s) == 0 }

// Reaches says s gets to the service named service.
func (s Scope) Reaches(service string) bool { return len(s) == 0 || slices.Contains(s, service) }

// Overlaps says some service gets both s and o (the whole app overlaps with
// everything).
func (s Scope) Overlaps(o Scope) bool {
	if len(s) == 0 || len(o) == 0 {
		return true
	}
	for _, x := range s {
		if slices.Contains(o, x) {
			return true
		}
	}
	return false
}

// Covers says every service o reaches gets s too.
func (s Scope) Covers(o Scope) bool {
	if len(s) == 0 {
		return true
	}
	if len(o) == 0 {
		return false
	}
	for _, x := range o {
		if !slices.Contains(s, x) {
			return false
		}
	}
	return true
}

// Equal says s and o reach the same services (both normalized).
func (s Scope) Equal(o Scope) bool { return slices.Equal(s.Normalize(), o.Normalize()) }

// Check validates a scope being written in field: service names, at most
// project.MaxServices, each one a service the app has now (services). The
// whole app is always valid.
func (s Scope) Check(field string, services []project.Service) error {
	if len(s) > project.MaxServices {
		return errx.InvalidField(field, "a setting names at most 8 services")
	}
	for _, name := range s {
		if !project.ValidServiceName(name) {
			return errx.InvalidField(field, "services are names of the app's services: "+name)
		}
		if !slices.ContainsFunc(services, func(x project.Service) bool { return x.Name == name }) {
			return errx.InvalidField(field, "the app has no service named "+name)
		}
	}
	return nil
}

// CheckNames validates the names only (a scope that removes something may
// name a service the app no longer has).
func (s Scope) CheckNames(field string) error {
	if len(s) > project.MaxServices {
		return errx.InvalidField(field, "a setting names at most 8 services")
	}
	for _, name := range s {
		if !project.ValidServiceName(name) {
			return errx.InvalidField(field, "services are names of the app's services: "+name)
		}
	}
	return nil
}

// Label words s for summaries: "" for the whole app, " (solo api y web)".
func (s Scope) Label() string {
	if len(s) == 0 {
		return ""
	}
	return " (solo " + joinAnd(s) + ")"
}

// Gate is the app's services s reaches, as IT's rules see them (the
// `services` of connect_resource and enable_ai).
func (s Scope) Gate(services []project.Service) []gate.Service {
	reached := make([]project.Service, 0, len(services))
	for _, x := range services {
		if s.Reaches(x.Name) {
			reached = append(reached, x)
		}
	}
	return project.GateServices(reached)
}

// Kept is the scope an input asks for: in when given (nil pointer: keep
// current).
func Kept(in *Scope, current Scope) Scope {
	if in == nil {
		return current.Normalize()
	}
	return in.Normalize()
}

// CheckResourceScope is the rule of a files resource (a ReadWriteOnce
// volume): in an app of two or more services it reaches exactly one, since
// two Deployments cannot mount it on different nodes. services are the
// app's now.
func CheckResourceScope(kind resourcekind.Kind, scope Scope, services []project.Service) error {
	if !resourcekind.Of(kind).Mounted || len(services) < 2 || len(scope) == 1 {
		return nil
	}
	return errx.InvalidField("services", "un volumen de archivos lo monta un solo servicio: elige cuál")
}

func joinAnd(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " y " + names[len(names)-1]
}

// holding is one source of a variable name: what holds it and for which
// services.
type holding struct {
	kind   holdingKind
	holder string // in the words of VARIABLE_TAKEN
	scope  Scope
	// layer is the layer of a plain variable: "" for the whole app's, the
	// service's name for one service's own.
	layer string
}

type holdingKind int

const (
	heldByVariable holdingKind = iota + 1
	heldBySecret
	heldByRequest
	heldByResource
	heldByAI
)

const (
	holderAppVar     = "una variable de la app"
	holderSecret     = "un secreto de la app"
	holderOpenSecret = "un secreto pendiente de la app"
)

func holderServiceVar(service string) string { return "una variable del servicio " + service }

// holdings maps every variable name the settings define (and the open
// requests ask for) to what holds it.
func (s Settings) holdings(open []SecretRequest) map[string][]holding {
	out := map[string][]holding{}
	for k := range s.Env {
		out[k] = append(out[k], holding{kind: heldByVariable, holder: holderAppVar})
	}
	for svc, env := range s.ServiceEnv {
		for k := range env {
			out[k] = append(out[k], holding{kind: heldByVariable, holder: holderServiceVar(svc), scope: Scope{svc}, layer: svc})
		}
	}
	for _, r := range s.Secrets {
		out[r.Name] = append(out[r.Name], holding{kind: heldBySecret, holder: holderSecret, scope: r.Services})
	}
	for _, u := range s.Resources {
		for _, v := range u.Variables() {
			out[v] = append(out[v], holding{kind: heldByResource, holder: u.Name, scope: u.Services})
		}
	}
	if s.AI.Enabled {
		for _, v := range AIVariables {
			out[v] = append(out[v], holding{kind: heldByAI, holder: aiHolder, scope: s.AI.Services})
		}
	}
	for _, q := range open {
		if _, secret := s.Secret(q.Name); !secret {
			out[q.Name] = append(out[q.Name], holding{kind: heldByRequest, holder: holderOpenSecret, scope: q.Services})
		}
	}
	return out
}

// clash is the first of held, of a kind other than the skipped ones, whose
// services meet scope (nil when none).
func clash(held []holding, scope Scope, skip ...holdingKind) *holding {
	for i, h := range held {
		if slices.Contains(skip, h.kind) {
			continue
		}
		if h.scope.Overlaps(scope) {
			return &held[i]
		}
	}
	return nil
}
