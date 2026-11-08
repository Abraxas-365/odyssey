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

// CanProvision reports whether the cluster lists kind in ResourceKinds:
// its built-in engine prepares it.
func (c Capabilities) CanProvision(kind resourcekind.Kind) bool {
	return slices.Contains(c.ResourceKinds, string(kind))
}

// CanConnect reports whether the cluster can wire an app to a resource of
// kind: its built-in engine prepares it, or the kind can be one the company
// already has and the agent writes such connections (the resource itself
// is checked by CheckResource, with its recipe's stores).
func (c Capabilities) CanConnect(kind resourcekind.Kind) bool {
	return c.CanProvision(kind) || (resourcekind.Of(kind).Existing && slices.Contains(c.ResourceCapabilities, resourcespec.ConnectExisting))
}

// Resources are every resource capability the cluster announces: the
// built-in engine of each kind it lists (resourcespec.Builtin, what an agent
// older than the catalog means by listing a kind), its ResourceCapabilities
// and the secret stores it reads from (resourcespec.SecretStore), sorted
// and without repeats.
func (c Capabilities) Resources() []string {
	out := make([]string, 0, len(c.ResourceKinds)+len(c.ResourceCapabilities)+len(c.SecretStores))
	for _, k := range c.ResourceKinds {
		out = append(out, resourcespec.Builtin(resourcekind.Kind(k)))
	}
	out = append(out, c.ResourceCapabilities...)
	for _, s := range c.SecretStores {
		out = append(out, resourcespec.SecretStore(s))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckResource is ApplyResource's guard (and a move's, for every resource
// of the environment): the cluster must honour everything the spec
// requires. A built-in resource of a kind the cluster does not list is
// CodeResourcesUnavailable as always; any other missing capability is
// CodeResourcesUnsupported, naming it. A resource the company already has
// needs no built-in engine: only the existing engine and its stores.
func (c Capabilities) CheckResource(kind resourcekind.Kind, spec resourcespec.Spec) error {
	if spec.Existing == nil && !c.CanProvision(kind) {
		return errx.Business(ResourcesUnavailableMessage(kind)).WithCode(CodeResourcesUnavailable).WithDetail("kind", string(kind))
	}
	missing := resourcespec.Missing(spec.Requires(kind), c.Resources())
	if len(missing) == 0 {
		return nil
	}
	msg := "El clúster de este entorno no puede preparar " + resourcekind.Of(kind).The() +
		" como lo pide su receta: su agente necesita una actualización (" + strings.Join(missing, ", ") + ")."
	if spec.Existing != nil {
		msg = ExistingUnsupportedMessage(kind, missing)
	}
	return errx.Business(msg).WithCode(CodeResourcesUnsupported).WithDetail("kind", string(kind)).WithDetail("missing", strings.Join(missing, ","))
}

// ExistingUnsupportedMessage is why a cluster cannot connect a resource the
// company already has: its agent does not write such connections (or the
// cluster has no External Secrets), or it lost a secret store the recipe
// reads from. Fixed phrases: the store's name is IT's, the rest is ours.
func ExistingUnsupportedMessage(kind resourcekind.Kind, missing []string) string {
	if slices.Contains(missing, resourcespec.ConnectExisting) {
		return "El clúster de este entorno no puede conectar " + resourcekind.Of(kind).The() +
			" de la empresa: necesita External Secrets y un agente actualizado. Pide a TI que lo revise."
	}
	var stores []string
	for _, m := range missing {
		if s, ok := strings.CutPrefix(m, resourcespec.SecretStore("")); ok {
			stores = append(stores, "«"+s+"»")
		}
	}
	return "El clúster de este entorno no tiene el almacén de secretos " + strings.Join(stores, ", ") +
		" de donde se leen las credenciales de " + resourcekind.Of(kind).The() + ". Pide a TI que lo revise."
}

// CodeResourcesUnsupported: the cluster provisions the kind but not as the
// resource's recipe asks (its agent lacks a capability the spec requires).
const CodeResourcesUnsupported = "RESOURCES_UNSUPPORTED"

// CheckResources is Apply's guard: a cluster that cannot provision or
// connect a kind must not get an app that waits for it.
func (c Capabilities) CheckResources(refs []ResourceRef) error {
	for _, r := range refs {
		if !c.CanConnect(r.Kind) {
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
	// StorageGi is the volume's size; it only grows. 0: a resource the
	// company already has (Spec.Existing), which has no size.
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
	case r.Spec.Existing != nil && r.StorageGi != 0:
		return errx.Validation("a resource the company already has has no size")
	case r.Spec.Existing == nil && !r.Deleted && (r.StorageGi < resourcekind.MinStorageGi || r.StorageGi > resourcekind.MaxStorageGi),
		r.StorageGi < 0 || r.StorageGi > resourcekind.MaxStorageGi:
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
