package fleet

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// Resources (2b). A resource (a database, a cache, a files volume) belongs to
// an environment. The resource domain decides it; the fleet places it on the
// environment's cluster like an app, carries it through the tunnel and keeps
// what the agent reports. Its credentials are made in the cluster and never
// leave it. Data does not move with the environment: after a move the new
// cluster prepares an empty resource.

// ResourceRef is a resource an app uses (App.Resources): the agent wires its
// connection into the app's environment and waits for it to be ready.
type ResourceRef struct {
	Name string            `json:"name"`
	Kind resourcekind.Kind `json:"kind"`
	// Prefix goes before the variables the app reads it by ("" for none).
	Prefix string `json:"prefix,omitempty"`
}

// MaxAppResources bounds the resources an app uses.
const MaxAppResources = 10

// CodeResourcesUnavailable: the environment's cluster does not list the kind
// of a resource in Capabilities.ResourceKinds (an agent older than 2b, no
// CloudNativePG, no storage class).
const CodeResourcesUnavailable = "RESOURCES_UNAVAILABLE"

// ResourcesUnavailableMessage is the plain-language text of
// CodeResourcesUnavailable for kind.
func ResourcesUnavailableMessage(kind resourcekind.Kind) string {
	switch kind {
	case resourcekind.Postgres:
		return "Este clúster no tiene bases de datos administradas. Pide a TI que instale CloudNativePG."
	case resourcekind.Redis:
		return "Este clúster no puede crear cachés. Pide a TI que actualice su agente y configure una clase de almacenamiento."
	case resourcekind.Files:
		return "Este clúster no puede crear volúmenes de archivos. Pide a TI que configure una clase de almacenamiento por defecto."
	}
	return "Este clúster no puede crear este tipo de recurso. Pide a TI que lo revise."
}

// CanProvision reports whether the cluster lists kind in ResourceKinds.
func (c Capabilities) CanProvision(kind resourcekind.Kind) bool {
	return slices.Contains(c.ResourceKinds, string(kind))
}

// CheckResources is Apply's guard: a cluster that cannot provision a kind
// must not get an app (or a resource) that waits for it.
func (c Capabilities) CheckResources(refs []ResourceRef) error {
	for _, r := range refs {
		if !c.CanProvision(r.Kind) {
			return errx.Business(ResourcesUnavailableMessage(r.Kind)).WithCode(CodeResourcesUnavailable).WithDetail("kind", string(r.Kind))
		}
	}
	return nil
}

// DesiredResource is the desired state of one resource of an environment.
type DesiredResource struct {
	Organization identity.OrganizationID `json:"organization_id" db:"organization_id"`
	Project      project.ID              `json:"project_id" db:"project_id"`
	Environment  project.EnvironmentID   `json:"environment_id" db:"environment_id"`
	// Cluster is where the environment runs (set by placement).
	Cluster ClusterID `json:"cluster_id" db:"cluster_id"`
	Name    string    `json:"name" db:"name"`
	// Kind never changes.
	Kind resourcekind.Kind `json:"kind" db:"kind"`
	// Generation grows with every change; an older one never replaces a
	// newer one, here or in the cluster.
	Generation int64 `json:"generation" db:"generation"`
	// StorageGi is the volume's size; it only grows.
	StorageGi int `json:"storage_gi" db:"storage_gi"`
	// Deleted: the resource and its data are to be removed.
	Deleted   bool      `json:"deleted" db:"deleted"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// Validate guards the fleet's invariants of a desired resource.
func (r DesiredResource) Validate() error {
	switch {
	case r.Organization.IsZero() || r.Project.IsZero() || r.Environment.IsZero():
		return errx.Validation("resource needs organization, project and environment")
	case resourcekind.CheckName(r.Name) != nil:
		return errx.Validation(resourcekind.CheckName(r.Name).Error())
	case !r.Kind.Valid():
		return errx.Validation("resource kind must be postgres, redis or files")
	case r.Generation < 1:
		return errx.Validation("resource generation must be positive")
	case r.StorageGi < resourcekind.MinStorageGi || r.StorageGi > resourcekind.MaxStorageGi:
		return errx.Validation(fmt.Sprintf("resource size is between %dGi and %dGi", resourcekind.MinStorageGi, resourcekind.MaxStorageGi))
	}
	return nil
}

// Phases of a resource, as the agent reports them (proto ResourceStatus.phase).
type ResourcePhase string

const (
	ResourceProvisioning ResourcePhase = "provisioning"
	ResourceReady        ResourcePhase = "ready"
	ResourceFailed       ResourcePhase = "failed"
	// ResourceDeleted: gone from the cluster; the control plane forgets it.
	ResourceDeleted ResourcePhase = "deleted"
)

func (p ResourcePhase) Valid() bool {
	return p == ResourceProvisioning || p == ResourceReady || p == ResourceFailed || p == ResourceDeleted
}

// ResourceStatus is what the agent observed of a resource.
type ResourceStatus struct {
	Environment project.EnvironmentID `json:"environment_id" db:"environment_id"`
	Name        string                `json:"name" db:"name"`
	// ObservedGeneration is the desired generation the status describes (0:
	// the agent has not acted on it yet).
	ObservedGeneration int64         `json:"observed_generation" db:"observed_generation"`
	Phase              ResourcePhase `json:"phase" db:"phase"`
	Message            string        `json:"message" db:"message"`
	UpdatedAt          time.Time     `json:"updated_at" db:"observed_at"`
}

// Validate bounds what an agent reports of a resource.
func (s ResourceStatus) Validate() error {
	switch {
	case s.Environment.IsZero() || resourcekind.CheckName(s.Name) != nil:
		return errx.Validation("reported resource needs an environment and a valid name")
	case s.ObservedGeneration < 0:
		return errx.Validation("reported generation must not be negative")
	case !s.Phase.Valid():
		return errx.Validation("reported resource phase is not valid")
	case utf8.RuneCountInString(s.Message) > MaxMessage || text.HasForbiddenControl(s.Message):
		return errx.Validation("reported message is too long or has control characters")
	}
	return nil
}

// MaxReportResources bounds the resources in one report.
const MaxReportResources = 1000

// ResourceReport is a batch of resource statuses from an agent. A full one
// lists every resource of the cluster (sent once per tunnel).
type ResourceReport struct {
	Full      bool
	Resources []ResourceStatus
}

// Validate checks the batch itself.
func (r ResourceReport) Validate() error {
	if len(r.Resources) > MaxReportResources {
		return errx.Validation("too many resources in one report")
	}
	return nil
}

// Valid is the report without its invalid statuses, and why the last of
// those was refused (nil when all are valid).
func (r ResourceReport) Valid() (ResourceReport, error) {
	out := ResourceReport{Full: r.Full, Resources: make([]ResourceStatus, 0, len(r.Resources))}
	var invalid error
	dropped := 0
	for _, s := range r.Resources {
		if err := s.Validate(); err != nil {
			invalid, dropped = err, dropped+1
			continue
		}
		out.Resources = append(out.Resources, s)
	}
	if invalid != nil {
		invalid = errx.Wrap(invalid, fmt.Sprintf("%d resource status(es) dropped from the report", dropped), errx.TypeValidation)
	}
	return out, invalid
}
