package fleet

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/resourcespec"
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
	// Services are wired to it (MS-4); empty: every service of the app.
	Services []string `json:"services,omitempty"`
}

// MaxAppResources bounds the resources an app uses.
const MaxAppResources = 10

// CodeResourcesUnavailable: the environment's cluster does not list the kind
// of a resource in Capabilities.ResourceKinds (an agent older than 2b, no
// CloudNativePG, no storage class). The catalog refuses with the same code.
const CodeResourcesUnavailable = resourcekind.CodeUnavailable

// ResourcesUnavailableMessage is the plain-language text of
// CodeResourcesUnavailable for kind.
func ResourcesUnavailableMessage(kind resourcekind.Kind) string {
	return resourcekind.Of(kind).Unavailable
}

// CanProvision reports whether the cluster lists kind in ResourceKinds.
func (c Capabilities) CanProvision(kind resourcekind.Kind) bool {
	return slices.Contains(c.ResourceKinds, string(kind))
}

// Resources are every resource capability the cluster announces: the
// built-in engine of each kind it lists (resourcespec.Builtin, what an agent
// older than the catalog means by listing a kind) and its
// ResourceCapabilities, sorted and without repeats.
func (c Capabilities) Resources() []string {
	out := make([]string, 0, len(c.ResourceKinds)+len(c.ResourceCapabilities))
	for _, k := range c.ResourceKinds {
		out = append(out, resourcespec.Builtin(resourcekind.Kind(k)))
	}
	out = append(out, c.ResourceCapabilities...)
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckResource is ApplyResource's guard (and a move's, for every resource
// of the environment): the cluster must provision the kind and honour
// everything the spec requires. A missing kind is CodeResourcesUnavailable
// as always; a missing capability is CodeResourcesUnsupported, naming it.
func (c Capabilities) CheckResource(kind resourcekind.Kind, spec resourcespec.Spec) error {
	if !c.CanProvision(kind) {
		return errx.Business(ResourcesUnavailableMessage(kind)).WithCode(CodeResourcesUnavailable).WithDetail("kind", string(kind))
	}
	if missing := resourcespec.Missing(spec.Requires(kind), c.Resources()); len(missing) > 0 {
		return errx.Business("El clúster de este entorno no puede preparar "+resourcekind.Of(kind).The()+
			" como lo pide su receta: su agente necesita una actualización ("+strings.Join(missing, ", ")+").").
			WithCode(CodeResourcesUnsupported).WithDetail("kind", string(kind)).WithDetail("missing", strings.Join(missing, ","))
	}
	return nil
}

// CodeResourcesUnsupported: the cluster provisions the kind but not as the
// resource's recipe asks (its agent lacks a capability the spec requires).
const CodeResourcesUnsupported = "RESOURCES_UNSUPPORTED"

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
	// Recipe is the catalog recipe@version that resolved it ("" before the
	// catalog), shown in the cluster; the agent acts on Spec.
	Recipe string `json:"recipe,omitempty" db:"recipe"`
	// Spec is how the kind's engine prepares it (zero: the basic resource).
	// The cluster must announce everything Spec.Requires(Kind).
	Spec resourcespec.Spec `json:"spec" db:"-"`
	// Deleted: the resource and its data are to be removed.
	Deleted   bool      `json:"deleted" db:"deleted"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// MaxRecipeRef bounds DesiredResource.Recipe.
const MaxRecipeRef = 80

// Validate guards the fleet's invariants of a desired resource.
func (r DesiredResource) Validate() error {
	switch {
	case r.Organization.IsZero() || r.Project.IsZero() || r.Environment.IsZero():
		return errx.Validation("resource needs organization, project and environment")
	case resourcekind.CheckName(r.Name) != nil:
		return errx.Validation(resourcekind.CheckName(r.Name).Error())
	case !r.Kind.Valid():
		return errx.Validation("resource kind must be " + resourcekind.KindsLabel())
	case r.Generation < 1:
		return errx.Validation("resource generation must be positive")
	case r.StorageGi < resourcekind.MinStorageGi || r.StorageGi > resourcekind.MaxStorageGi:
		return errx.Validation(fmt.Sprintf("resource size is between %dGi and %dGi", resourcekind.MinStorageGi, resourcekind.MaxStorageGi))
	case len(r.Recipe) > MaxRecipeRef || text.HasForbiddenControl(r.Recipe):
		return errx.Validation("resource recipe is too long or has control characters")
	}
	if err := r.Spec.Validate(r.Kind); err != nil {
		return errx.Validation(err.Error())
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
