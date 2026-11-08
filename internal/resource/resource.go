// Package resource is the databases, caches and file volumes (2b) of an
// environment. The domain decides what exists (name, kind, size, its life:
// active → deleting → forgotten); where and how it runs is the fleet's job
// (Cluster port), and which app uses it is release's (Usage port). What the
// cluster observes of a resource (preparing, ready, failed) is read from the
// fleet when asked and never stored here.
//
// Data does not move with an environment: after the environment changes
// cluster, the new cluster prepares the resource empty.
package resource

import (
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
)

// MaxPerEnvironment bounds the resources of one environment (those being
// deleted still count: they still hold space in the cluster).
const MaxPerEnvironment = 10

// Codes of resources.
const (
	// CodeLimit: the environment already has MaxPerEnvironment resources.
	CodeLimit = "RESOURCE_LIMIT"
	// CodeInUse: the resource is connected to the app and cannot be deleted.
	CodeInUse = "RESOURCE_IN_USE"
	// CodeDeleting: the resource is being deleted and takes no changes.
	CodeDeleting = "RESOURCE_DELETING"
	// CodeChanged: the resource changed between judging a change and
	// applying it; asking again judges what is there now.
	CodeChanged = "RESOURCE_CHANGED"
)

// Status is where a resource is in its life. A deleted resource is
// forgotten (its row is removed once the cluster erased the data).
type Status string

const (
	StatusActive   Status = "active"
	StatusDeleting Status = "deleting"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusDeleting }

// Resource is a database, cache or volume of an environment.
type Resource struct {
	ID           ID                      `json:"id" db:"id"`
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	// Name is unique among the environment's resources.
	Name string            `json:"name" db:"name"`
	Kind resourcekind.Kind `json:"kind" db:"kind"`
	// StorageGi is the volume's size in GiB; it only grows. It is
	// Params[catalog.ParamStorage], kept in its own column while consumers
	// move to Params (the table checks they agree).
	StorageGi int `json:"storage_gi" db:"storage_gi"`
	// Recipe and RecipeVersion are the catalog recipe the resource is
	// pinned to (docs/catalog.md); a resource from before the catalog is
	// its kind's basic recipe, version 1.
	Recipe        string `json:"recipe" db:"recipe"`
	RecipeVersion int    `json:"recipe_version" db:"recipe_version"`
	// Params are the recipe's parameters, normalized: the source of truth.
	Params map[string]int `json:"params" db:"-"`
	// Generation grows with every change; the cluster ignores an older one.
	Generation int64           `json:"generation" db:"generation"`
	Status     Status          `json:"status" db:"status"`
	CreatedBy  identity.UserID `json:"created_by" db:"created_by"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at" db:"updated_at"`
}

// KindLabel names a kind in the words people use (for activity summaries).
func KindLabel(k resourcekind.Kind) string { return resourcekind.Of(k).Noun }

// Phase is how the cluster sees a resource (what the agent reported).
type Phase string

const (
	PhaseProvisioning Phase = "provisioning" // being prepared (also: nothing reported yet)
	PhaseReady        Phase = "ready"
	PhaseFailed       Phase = "failed"
	PhaseDeleted      Phase = "deleted"
)

// Observed is what the cluster last reported of a resource.
type Observed struct {
	Phase   Phase  `json:"phase"`
	Message string `json:"message"`
}

// Connection is how the environment's app is connected to a resource.
type Connection struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// Services get its variables (MS-4); empty: the whole app.
	Services []string `json:"services"`
}

// Link is a connection being made.
type Link struct {
	Name   string
	Prefix string
	// Services are the services it reaches (nil: as the connection says,
	// the whole app for a new one; empty: the whole app).
	Services *[]string
	// Judged: IT's rules were already asked (Usage.JudgeConnect, settled by
	// resource), so connecting does not ask again.
	Judged bool
}

// Ask is a connection IT's rules are asked about before it is made
// (Usage.JudgeConnect): the resource (it may not exist yet) and the
// services it would reach (as in Link).
type Ask struct {
	Name     string
	Kind     resourcekind.Kind
	Services *[]string
}

// Applied is what connecting or disconnecting did in release: the settings
// revision stored and the config deployment started (nil: nothing is online,
// or the change was already in force).
type Applied struct {
	Revision   int64
	Deployment *string
}

// View is a resource as people and assistants see it: the row, what the
// cluster reports and how the app uses it.
type View struct {
	Resource
	Phase   Phase  `json:"phase"`
	Message string `json:"message"`
	// Connected says whether the app receives the resource's variables;
	// Prefix goes before their names; Services get them (empty: the whole
	// app).
	Connected bool     `json:"connected"`
	Prefix    string   `json:"prefix"`
	Services  []string `json:"services"`
	// Variables are the names the app reads the resource by: with the
	// connection's prefix, or without one while not connected.
	Variables []string `json:"variables"`
	// Outcome: the action was held for IT's approval and did not happen (the
	// rest of the view is then empty); the transport answers 202.
	gate.Outcome
}

// NewView puts a resource together with what is observed and its connection.
func NewView(r Resource, o Observed, c *Connection) View {
	v := View{Resource: r, Phase: o.Phase, Message: o.Message, Services: []string{}}
	if v.Phase == "" {
		v.Phase = PhaseProvisioning
	}
	if c != nil {
		v.Connected, v.Prefix = true, c.Prefix
		v.Services = append(v.Services, c.Services...)
	}
	v.Variables = resourcekind.Variables(r.Kind, v.Prefix)
	return v
}

// Listing is an environment's resources and what its cluster can provision.
type Listing struct {
	Items []View `json:"items"`
	// Kinds are the kinds the environment's cluster can prepare (none when it
	// has no cluster, or it was revoked).
	Kinds []resourcekind.Kind `json:"available_kinds"`
}

// Desired is a resource as the cluster must have it.
type Desired struct {
	Organization identity.OrganizationID
	Project      project.ID
	Environment  project.EnvironmentID
	Name         string
	Kind         resourcekind.Kind
	Generation   int64
	StorageGi    int
	// Recipe is the recipe@version that prepares it (shown in the cluster).
	Recipe string
	// Spec is how its engine prepares it, fixed by that version (zero: the
	// basic resource).
	Spec resourcespec.Spec
	// Deleted: remove the resource and its data.
	Deleted bool
}

// DesiredOf is the cluster's desired state for r. Its spec is the one its
// pinned recipe version fixes; a version this binary does not know is an
// internal error, never a basic resource in its place (a database meant to
// have replicas must not quietly lose them). A resource being deleted needs
// no spec (the agent only removes it), so it can always be deleted.
func DesiredOf(r Resource) (Desired, error) {
	d := Desired{Organization: r.Organization, Project: r.Project, Environment: r.Environment, Name: r.Name, Kind: r.Kind,
		Generation: r.Generation, StorageGi: r.StorageGi, Deleted: r.Status == StatusDeleting}
	v, ok := catalog.Version(r.Recipe, r.RecipeVersion)
	switch {
	case ok:
		d.Recipe, d.Spec = v.Ref(), v.Spec
	case !d.Deleted:
		return Desired{}, errx.Internal("resource " + r.Name + " is pinned to " + r.Recipe + "@" + strconv.Itoa(r.RecipeVersion) +
			", which this version of Odicea does not know")
	}
	return d, nil
}

// Create is the request to add a resource to an environment.
type Create struct {
	Name string            `json:"name"`
	Kind resourcekind.Kind `json:"kind"`
	// Recipe fulfils the kind ("": the kind's default in the environment's
	// tier, docs/catalog.md).
	Recipe string `json:"recipe"`
	// RecipeVersion pins the recipe's version (0: the offered one). Only an
	// approved request re-running sets it: what IT approved is what runs.
	RecipeVersion int `json:"-"`
	// Params are the recipe's parameters (absent: the offering's defaults).
	Params map[string]int `json:"params"`
	// StorageGi is params.storage_gi, as before the catalog (0: absent).
	StorageGi int `json:"storage_gi"`
	// Connect, when present, connects the resource to the app in the same
	// operation.
	Connect     *ConnectOptions `json:"connect"`
	RequestedBy identity.UserID `json:"-"`
}

// ConnectOptions are how a resource is connected.
type ConnectOptions struct {
	Prefix string `json:"prefix"`
	// Services get its variables (absent or empty: the whole app; MS-4).
	Services *[]string `json:"services"`
}

// Normalize folds storage_gi into params (the catalog fills the defaults).
func (in Create) Normalize() Create {
	in.Name, in.Recipe = strings.TrimSpace(in.Name), strings.TrimSpace(in.Recipe)
	in.Params = foldStorage(in.Params, in.StorageGi)
	if in.Connect != nil {
		c := ConnectOptions{Prefix: strings.TrimSpace(in.Connect.Prefix), Services: in.Connect.Services}
		in.Connect = &c
	}
	return in
}

// foldStorage is params with storage_gi set from the legacy field (a copy).
func foldStorage(params map[string]int, storageGi int) map[string]int {
	out := maps.Clone(params)
	if out == nil {
		out = map[string]int{}
	}
	if _, set := out[catalog.ParamStorage]; storageGi != 0 && !set {
		out[catalog.ParamStorage] = storageGi
	}
	return out
}

// MaxParams bounds the parameters a request may carry.
const MaxParams = 32

func (in Create) Validate() error {
	if err := resourcekind.CheckName(in.Name); err != nil {
		return errx.InvalidField("name", err.Error())
	}
	if !in.Kind.Valid() {
		return errx.InvalidField("kind", "kind must be "+resourcekind.KindsLabel())
	}
	if err := checkParams(in.Params, in.StorageGi); err != nil {
		return err
	}
	if in.Connect != nil {
		if err := resourcekind.CheckPrefix(in.Connect.Prefix); err != nil {
			return errx.InvalidField("connect.prefix", err.Error())
		}
	}
	return nil
}

// checkParams: a bounded set, and storage_gi said once (the catalog checks
// the names and the values against the recipe and the offering).
func checkParams(params map[string]int, storageGi int) error {
	if len(params) > MaxParams {
		return errx.InvalidField("params", "a recipe has at most "+strconv.Itoa(MaxParams)+" parameters")
	}
	if storageGi < 0 {
		return errx.InvalidField("storage_gi", "storage_gi is a size in GiB: at least 1")
	}
	if v, set := params[catalog.ParamStorage]; storageGi != 0 && set && v != storageGi {
		return errx.InvalidField("storage_gi", "storage_gi and params.storage_gi say different sizes")
	}
	return nil
}

// Change is the request to change a resource's parameters (it keeps its
// recipe@version; what each parameter allows — grow only, never change —
// is the recipe's, docs/catalog.md).
type Change struct {
	// Params are the values asked for (absent: unchanged).
	Params map[string]int `json:"params"`
	// StorageGi is params.storage_gi, as before the catalog (0: absent).
	StorageGi int `json:"storage_gi"`
	// Recipe and RecipeVersion pin what an approved change was judged on
	// ("": not pinned). Only an approved request re-running sets them.
	Recipe        string          `json:"-"`
	RecipeVersion int             `json:"-"`
	RequestedBy   identity.UserID `json:"-"`
}

func (in Change) Normalize() Change {
	in.Params = foldStorage(in.Params, in.StorageGi)
	return in
}

func (in Change) Validate() error {
	if err := checkParams(in.Params, in.StorageGi); err != nil {
		return err
	}
	if len(foldStorage(in.Params, in.StorageGi)) == 0 {
		return errx.InvalidField("params", "say which parameter changes")
	}
	return nil
}

// Delete is the request to delete a resource and its data.
type Delete struct {
	RequestedBy identity.UserID
}

// Connect is the request to connect a resource to the app.
type Connect struct {
	Name   string `json:"-"`
	Prefix string `json:"prefix"`
	// Services get its variables (absent: as the connection says, the
	// whole app for a new one; empty: the whole app; MS-4).
	Services    *[]string       `json:"services"`
	RequestedBy identity.UserID `json:"-"`
}

func (in Connect) Normalize() Connect {
	in.Name, in.Prefix = strings.TrimSpace(in.Name), strings.TrimSpace(in.Prefix)
	return in
}

func (in Connect) Validate() error {
	if err := resourcekind.CheckName(in.Name); err != nil {
		return errx.InvalidField("name", err.Error())
	}
	if err := resourcekind.CheckPrefix(in.Prefix); err != nil {
		return errx.InvalidField("prefix", err.Error())
	}
	return nil
}

// Result is a change to a resource and the deployment it started, if any.
type Result struct {
	View
	// Revision is the settings revision a connection change stored (0: none).
	Revision int64 `json:"revision"`
	// Deployment is the config deployment that applies the change.
	Deployment *string `json:"deployment_id"`
}

// ErrInUse is the 409 for deleting a resource the app uses.
func ErrInUse(name string) error {
	return errx.Conflict("El recurso "+name+" está conectado a la app. Desconéctalo primero.").
		WithCode(CodeInUse).WithDetail("name", name)
}

// ErrDeleting is the 409 for changing a resource that is being deleted.
func ErrDeleting(name string) error {
	return errx.Conflict("El recurso "+name+" se está eliminando.").WithCode(CodeDeleting).WithDetail("name", name)
}

// ErrChangedMeanwhile is the 409 for a change judged on a resource that
// changed before it was applied.
func ErrChangedMeanwhile(name string) error {
	return errx.Conflict("El recurso "+name+" cambió mientras se revisaba el pedido. Vuelve a intentarlo.").
		WithCode(CodeChanged).WithDetail("name", name)
}

// ErrLimit is the 409 for an environment that has all the resources it may.
func ErrLimit() error {
	return errx.Conflict("Un entorno puede tener hasta 10 recursos. Elimina uno para agregar otro.").WithCode(CodeLimit)
}

func NotFound() error { return errx.NotFound("resource not found") }
