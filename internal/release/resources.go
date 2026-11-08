package release

import (
	"slices"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Resources of an environment (2b). The resource domain owns the databases,
// caches and volumes; release only records which of them the app is
// connected to, as part of each settings revision (Settings.Resources), so a
// deployment applies exactly the connections of its revision. The variables
// a connection defines (resourcekind.Variables) are never stored here: the
// agent builds them in the cluster, where the credentials live.

// MaxResourceUses bounds the resources one app is connected to (the same
// bound the fleet applies to an App).
const MaxResourceUses = 10

// CodeVariableTaken: a variable name is already used by another variable,
// secret or resource of the environment.
const CodeVariableTaken = "VARIABLE_TAKEN"

// CodeResourceDeleting: a resource being deleted cannot be connected.
const CodeResourceDeleting = "RESOURCE_DELETING"

// ResourceUse is a resource the app is connected to.
type ResourceUse struct {
	Name string            `json:"name"`
	Kind resourcekind.Kind `json:"kind"`
	// Prefix goes before the variable names ("" for none).
	Prefix string `json:"prefix"`
	// Services get its variables (MS-4); empty: the whole app.
	Services Scope `json:"services,omitempty"`
}

// Variables are the names the app reads the resource by.
func (u ResourceUse) Variables() []string { return resourcekind.Variables(u.Kind, u.Prefix) }

func (u ResourceUse) equal(o ResourceUse) bool {
	return u.Name == o.Name && u.Kind == o.Kind && u.Prefix == o.Prefix && u.Services.Equal(o.Services)
}

// DesiredResource is a connection as the cluster receives it.
type DesiredResource struct {
	Name     string            `json:"name"`
	Kind     resourcekind.Kind `json:"kind"`
	Prefix   string            `json:"prefix"`
	Services []string          `json:"services,omitempty"`
}

// Resource is the connection to the resource named name (false: none).
func (s Settings) Resource(name string) (ResourceUse, bool) {
	for _, u := range s.Resources {
		if u.Name == name {
			return u, true
		}
	}
	return ResourceUse{}, false
}

// ErrVariableTaken is the 409 for a variable name something else holds.
func ErrVariableTaken(name, holder string) error {
	return errx.Conflict("La variable "+name+" ya la usa "+holder+". Elige otro nombre o un prefijo distinto.").
		WithCode(CodeVariableTaken).WithDetail("name", name)
}

// ConnectResource connects a resource of the environment to its app.
// Services are the services that get its variables (nil: the whole app, or
// as the connection already says).
type ConnectResource struct {
	Name        string          `json:"name"`
	Prefix      string          `json:"prefix"`
	Services    *Scope          `json:"services"`
	RequestedBy identity.UserID `json:"-"`
	// Judged says the caller already asked IT's rules about connecting this
	// kind (Usage.JudgeConnect) and settled the verdict, so ConnectResource
	// does not ask again. Only the resource domain sets it: it judges a
	// create (or a connection) as one operation, before it changes anything.
	Judged bool `json:"-"`
}

func (in ConnectResource) Validate() error {
	if err := resourcekind.CheckName(in.Name); err != nil {
		return errx.InvalidField("name", err.Error())
	}
	if err := resourcekind.CheckPrefix(in.Prefix); err != nil {
		return errx.InvalidField("prefix", err.Error())
	}
	return scopeNames(in.Services)
}

// JudgeConnect is a connection IT's rules are asked about before it is made
// (Usage.JudgeConnect): the resource (it may not exist yet) and the services
// it would reach (nil: as the connection says, the whole app for a new one).
type JudgeConnect struct {
	Name     string
	Kind     resourcekind.Kind
	Services *Scope
}

// DisconnectResource disconnects a resource from the app (it keeps existing).
type DisconnectResource struct {
	Name        string          `json:"name"`
	RequestedBy identity.UserID `json:"-"`
}

func (in DisconnectResource) Validate() error {
	if err := resourcekind.CheckName(in.Name); err != nil {
		return errx.InvalidField("name", err.Error())
	}
	return nil
}

// WithResource is the next settings with the resource connected under u
// (replacing the connection of the same name, whose prefix or services may
// change). It refuses a variable that a plain variable, a secret, an open
// secret request, another resource or the AI already holds for a service u
// reaches. changed is false when u was already the connection.
func (s Settings) WithResource(u ResourceUse, actor identity.UserID, open []SecretRequest) (next Settings, changed bool, err error) {
	u.Services = u.Services.Normalize()
	if old, ok := s.Resource(u.Name); ok && old.equal(u) {
		return s, false, nil
	}
	others := s
	others.Resources = slices.DeleteFunc(slices.Clone(s.Resources), func(r ResourceUse) bool { return r.Name == u.Name })
	held := others.holdings(open)
	for _, v := range u.Variables() {
		if h := clash(held[v], u.Services); h != nil {
			return Settings{}, false, ErrVariableTaken(v, h.holder)
		}
	}
	if len(others.Resources) >= MaxResourceUses {
		return Settings{}, false, errx.InvalidField("name", "an app uses at most 10 resources")
	}
	n := s.next(actor)
	n.Resources = append(slices.DeleteFunc(n.Resources, func(r ResourceUse) bool { return r.Name == u.Name }), u)
	if n.NameCount(open) > MaxSettingsVars {
		return Settings{}, false, ErrTooManyVars("name")
	}
	return n.Normalize(), true, nil
}

// WithoutResource is the next settings without the connection named name;
// changed is false when there was none.
func (s Settings) WithoutResource(name string, actor identity.UserID) (next Settings, changed bool) {
	if _, ok := s.Resource(name); !ok {
		return s, false
	}
	n := s.next(actor)
	n.Resources = slices.DeleteFunc(n.Resources, func(r ResourceUse) bool { return r.Name == name })
	return n, true
}

// ResourceInfo is what release needs to know of a resource to connect it.
type ResourceInfo struct {
	Name string
	Kind resourcekind.Kind
	// Deleting: the resource is being removed and cannot be connected.
	Deleting bool
}

// Resources changes (ResourcesChanged.Change).
const (
	ResourceConnected    = "connected"
	ResourceDisconnected = "disconnected"
)

// ResourcesChanged v1: a resource was connected to, or disconnected from, an
// environment's app. Never a value: the variables are made in the cluster.
type ResourcesChanged struct {
	Project     string        `json:"project_id"`
	Environment string        `json:"environment_id"`
	Revision    int64         `json:"revision"`
	Change      string        `json:"change"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	Prefix      string        `json:"prefix"`
	Deployment  *DeploymentID `json:"deployment_id"`
	// Services get its variables (MS-4, additive); absent: the whole app.
	Services []string `json:"services,omitempty"`
}

func NewResourcesChanged(org identity.OrganizationID, actor identity.UserID, projectID string, now Settings, change string, u ResourceUse, deployment *DeploymentID, n Names) event.Event {
	verb := "Conectó"
	if change == ResourceDisconnected {
		verb = "Desconectó"
	}
	return event.New(EventResourcesChanged, 1, org, actor, event.Subject(AccessSubjectKind, now.Environment),
		verb+" "+u.Name+u.Services.Label()+" de "+n.where(),
		ResourcesChanged{Project: projectID, Environment: now.Environment.String(), Revision: now.Revision, Change: change,
			Name: u.Name, Kind: string(u.Kind), Prefix: u.Prefix, Deployment: deployment, Services: u.Services})
}
