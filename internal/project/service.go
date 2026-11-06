package project

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// A Service is one workload of an app: the web of a monorepo, its API, a
// queue worker. An app has at least one; every app created before services
// existed has one, "web" at "/" (its whole code). The project owns the
// topology (which services, where their code is, which path of the app's
// address each one answers); how a service runs (port, health check,
// replicas, variables) belongs to each version of the app (release).

// ServiceKind is what a service is.
type ServiceKind string

const (
	// ServiceWeb answers HTTP: publicly under its Route, or only inside the
	// app when Route is "".
	ServiceWeb ServiceKind = "web"
	// ServiceWorker runs without a port (a queue consumer): no route, no
	// address, no health path.
	ServiceWorker ServiceKind = "worker"
)

func (k ServiceKind) Valid() bool { return k == ServiceWeb || k == ServiceWorker }

// ServiceKindMessage is the 400 text for a value outside the set.
const ServiceKindMessage = "kind must be web or worker"

// Limits of an app's services.
const (
	// MaxServices bounds the services of one app.
	MaxServices = 8
	// MaxServiceName bounds a service's name: "<app>-<service>" must stay a
	// DNS label (63) with the longest app name (MaxSlug + "-" + environment).
	MaxServiceName = 20
	// MaxRoot bounds a service's folder inside the app's code.
	MaxRoot = 200
	// MaxRoute bounds a service's path.
	MaxRoute = 100
	// DefaultService is the name of an app's first service.
	DefaultService = "web"
	// ReservedRoute is the platform's own path on every app's address (the
	// sign-in callback): no service may answer under it.
	ReservedRoute = "/_odicea"
)

// ServiceNameMessage is the 400 text of an invalid service name.
const ServiceNameMessage = "a service name is 1-20 lowercase letters and digits, starting with a letter (it becomes part of addresses and of variable names)"

var (
	serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,19}$`)
	routePattern       = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)
)

// ValidServiceName reports whether s is an acceptable service name.
func ValidServiceName(s string) bool { return serviceNamePattern.MatchString(s) }

// Service is one service of an app.
type Service struct {
	Organization identity.OrganizationID `json:"-" db:"organization_id"`
	Project      ID                      `json:"-" db:"project_id"`
	Name         string                  `json:"name" db:"name"`
	Kind         ServiceKind             `json:"kind" db:"kind"`
	// Root is the service's folder inside the app's code ("" = all of it).
	Root string `json:"root" db:"root"`
	// Route is the path of the app's address the service answers ("/" for
	// the whole address, "/api" for everything under /api); "" keeps a web
	// service internal (reachable only by the app's other services). A
	// worker never has one.
	Route string `json:"route" db:"route"`
	// StripPrefix removes Route from the path before the request reaches
	// the service (it then sees "/users" for "/api/users").
	StripPrefix bool      `json:"strip_prefix" db:"strip_prefix"`
	Position    int       `json:"-" db:"position"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// Public reports whether the service answers on the app's address.
func (s Service) Public() bool { return s.Kind == ServiceWeb && s.Route != "" }

// DefaultServices are the services of a new app: one web service with all
// of its code at the whole address.
func DefaultServices(organization identity.OrganizationID, id ID) []Service {
	return []Service{{Organization: organization, Project: id, Name: DefaultService, Kind: ServiceWeb, Route: "/"}}
}

// SetServices replaces an app's services (the whole list, in its order).
type SetServices struct {
	Items     []ServiceSpec   `json:"items"`
	UpdatedBy identity.UserID `json:"-"` // taken from the token, never the body
}

// ServiceSpec is one service as asked for.
type ServiceSpec struct {
	Name        string      `json:"name"`
	Kind        ServiceKind `json:"kind"`
	Root        string      `json:"root"`
	Route       string      `json:"route"`
	StripPrefix bool        `json:"strip_prefix"`
}

// NormalizeRoot trims slashes and spaces ("/apps/web/" → "apps/web").
func NormalizeRoot(root string) string { return strings.Trim(strings.TrimSpace(root), "/") }

// ValidRoot accepts "" or a relative folder path without "." or ".." parts.
func ValidRoot(root string) error {
	if root == "" {
		return nil
	}
	if len(root) > MaxRoot || !utf8.ValidString(root) || strings.ContainsAny(root, "\\\x00") {
		return errx.InvalidField("root", "root must be a folder of the code")
	}
	for _, part := range strings.Split(root, "/") {
		if part == "" || part == "." || part == ".." {
			return errx.InvalidField("root", "root must be a folder of the code")
		}
	}
	return nil
}

// NormalizeRoute trims spaces and a trailing slash ("/api/" → "/api"); "/"
// stays "/".
func NormalizeRoute(route string) string {
	route = strings.TrimSpace(route)
	if len(route) > 1 {
		route = strings.TrimRight(route, "/")
		if route == "" {
			route = "/"
		}
	}
	return route
}

// validRoute accepts "/" or a path of segments (letters, digits, . _ ~ -).
func validRoute(route string) error {
	switch {
	case route == "/":
		return nil
	case len(route) > MaxRoute || !routePattern.MatchString(route):
		return errx.InvalidField("route", "route must be / or a path like /api (letters, digits, . _ ~ -), up to 100 characters")
	case route == ReservedRoute || strings.HasPrefix(route, ReservedRoute+"/"):
		return errx.InvalidField("route", "routes under /_odicea belong to the platform")
	}
	for _, part := range strings.Split(route[1:], "/") {
		if part == "." || part == ".." {
			return errx.InvalidField("route", "route must not have . or .. segments")
		}
	}
	return nil
}

// Normalize trims every field; an empty kind is web.
func (s SetServices) Normalize() SetServices {
	items := make([]ServiceSpec, len(s.Items))
	for i, it := range s.Items {
		it.Name = strings.TrimSpace(it.Name)
		if it.Kind == "" {
			it.Kind = ServiceWeb
		}
		it.Root = NormalizeRoot(it.Root)
		it.Route = NormalizeRoute(it.Route)
		items[i] = it
	}
	s.Items = items
	return s
}

// Validate checks each service and the list as a whole: unique names, at
// most one service per route, no strip of "/".
func (s SetServices) Validate() error {
	if s.UpdatedBy.IsZero() {
		return errx.Validation("editor is required")
	}
	if len(s.Items) == 0 {
		return errx.InvalidField("items", "an app has at least one service")
	}
	if len(s.Items) > MaxServices {
		return errx.InvalidField("items", "an app has at most 8 services")
	}
	names, routes := map[string]bool{}, map[string]bool{}
	for _, it := range s.Items {
		if err := it.Validate(); err != nil {
			return err
		}
		if names[it.Name] {
			return errx.InvalidField("name", "two services are named "+it.Name)
		}
		names[it.Name] = true
		if it.Route != "" {
			if routes[it.Route] {
				return errx.InvalidField("route", "two services answer "+it.Route)
			}
			routes[it.Route] = true
		}
	}
	return nil
}

func (s ServiceSpec) Validate() error {
	if !ValidServiceName(s.Name) {
		return errx.InvalidField("name", ServiceNameMessage)
	}
	if !s.Kind.Valid() {
		return errx.InvalidField("kind", ServiceKindMessage)
	}
	if err := ValidRoot(s.Root); err != nil {
		return err
	}
	if s.Kind == ServiceWorker {
		if s.Route != "" || s.StripPrefix {
			return errx.InvalidField("route", "a worker has no route")
		}
		return nil
	}
	if s.Route == "" {
		if s.StripPrefix {
			return errx.InvalidField("strip_prefix", "an internal service has no route to strip")
		}
		return nil
	}
	if err := validRoute(s.Route); err != nil {
		return err
	}
	if s.StripPrefix && s.Route == "/" {
		return errx.InvalidField("strip_prefix", "there is nothing to strip from /")
	}
	return nil
}

// Services builds the stored services of a project from a validated input.
func (s SetServices) Services(organization identity.OrganizationID, id ID) []Service {
	out := make([]Service, len(s.Items))
	for i, it := range s.Items {
		out[i] = Service{Organization: organization, Project: id, Name: it.Name, Kind: it.Kind, Root: it.Root,
			Route: it.Route, StripPrefix: it.StripPrefix, Position: i}
	}
	return out
}

// SameServices reports whether two lists describe the same services in the
// same order (a PUT of the current list is a no-op).
func SameServices(a, b []Service) bool {
	return slices.EqualFunc(a, b, func(x, y Service) bool {
		return x.Name == y.Name && x.Kind == y.Kind && x.Root == y.Root && x.Route == y.Route && x.StripPrefix == y.StripPrefix
	})
}

// CodeServicesUnavailable: a cluster of the app cannot run more than one
// service yet (its agent does not list the services capability).
const CodeServicesUnavailable = "SERVICES_UNAVAILABLE"

// ServicesUnavailableMessage is the plain-language text of
// CodeServicesUnavailable.
const ServicesUnavailableMessage = "El clúster de esta app todavía no puede correr varios servicios. Pide a TI que actualice su agente."

// Simple reports whether any cluster agent can run the services as they
// are: one web service with all of the code at the whole address. Anything
// else needs the services capability (and, for a folder, the build per
// service).
func Simple(services []Service) bool {
	return len(services) == 1 && services[0].Kind == ServiceWeb && services[0].Route == "/" && !services[0].StripPrefix &&
		services[0].Root == ""
}
