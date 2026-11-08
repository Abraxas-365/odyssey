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
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
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
	// StorageGi is the volume's size in GiB; it only grows.
	StorageGi int `json:"storage_gi" db:"storage_gi"`
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
	// Judged: IT's rules were already asked (Usage.AuthorizeConnect), so
	// connecting does not ask again.
	Judged bool
}

// Ask is a connection IT's rules are asked about before it is made
// (Usage.AuthorizeConnect): the resource (it may not exist yet), the
// services it would reach (as in Link) and what a held one re-runs.
type Ask struct {
	Name     string
	Kind     resourcekind.Kind
	Services *[]string
	Detail   gate.Detail
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
	// Deleted: remove the resource and its data.
	Deleted bool
}

// DesiredOf is the cluster's desired state for r.
func DesiredOf(r Resource) Desired {
	return Desired{Organization: r.Organization, Project: r.Project, Environment: r.Environment, Name: r.Name, Kind: r.Kind,
		Generation: r.Generation, StorageGi: r.StorageGi, Deleted: r.Status == StatusDeleting}
}

// Create is the request to add a resource to an environment.
type Create struct {
	Name string            `json:"name"`
	Kind resourcekind.Kind `json:"kind"`
	// StorageGi is the size in GiB (default 1).
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

func (in Create) Normalize() Create {
	in.Name = strings.TrimSpace(in.Name)
	if in.StorageGi == 0 {
		in.StorageGi = resourcekind.DefaultStorageGi
	}
	if in.Connect != nil {
		c := ConnectOptions{Prefix: strings.TrimSpace(in.Connect.Prefix), Services: in.Connect.Services}
		in.Connect = &c
	}
	return in
}

func (in Create) Validate() error {
	if err := resourcekind.CheckName(in.Name); err != nil {
		return errx.InvalidField("name", err.Error())
	}
	if !in.Kind.Valid() {
		return errx.InvalidField("kind", "kind must be "+resourcekind.KindsLabel())
	}
	if err := CheckSize(in.StorageGi); err != nil {
		return err
	}
	if in.Connect != nil {
		if err := resourcekind.CheckPrefix(in.Connect.Prefix); err != nil {
			return errx.InvalidField("connect.prefix", err.Error())
		}
	}
	return nil
}

// CheckSize says whether gi is a size a resource can have.
func CheckSize(gi int) error {
	if gi < resourcekind.MinStorageGi || gi > resourcekind.MaxStorageGi {
		return errx.InvalidField("storage_gi", "the size is between 1 and 100 GiB")
	}
	return nil
}

// Resize is the request to grow a resource.
type Resize struct {
	StorageGi   int             `json:"storage_gi"`
	RequestedBy identity.UserID `json:"-"`
}

func (in Resize) Validate() error { return CheckSize(in.StorageGi) }

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

// ErrLimit is the 409 for an environment that has all the resources it may.
func ErrLimit() error {
	return errx.Conflict("Un entorno puede tener hasta 10 recursos. Elimina uno para agregar otro.").WithCode(CodeLimit)
}

func NotFound() error { return errx.NotFound("resource not found") }
