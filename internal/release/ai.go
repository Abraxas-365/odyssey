package release

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// CodeAIOff: choosing the services of an AI that is off.
const CodeAIOff = "AI_NOT_ENABLED"

// AI of an environment (2d-ai). The ai domain owns the keys; release only
// records, as part of each settings revision (Settings.AI), that the app uses
// the organization's AI gateway and which key version it waits for, so a
// deployment applies exactly the AI of its revision. The key itself travels
// sealed to the cluster as the managed platform secret AIKeySecret and is
// never stored here.

// AIKeySecret is the managed secret that holds the app's AI key. The name is
// the platform's (ODICEA_ is reserved): nobody can set it by hand.
const AIKeySecret = "ODICEA_AI_KEY"

// AIVariables are the names the agent sets from the key and the gateway
// address; they win over anything the app defines, and the control plane
// refuses to define them while AI is on (VARIABLE_TAKEN).
var AIVariables = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}

const aiHolder = "la IA de la app"

// AISettings is the AI part of an environment's settings. It carries what
// the desired app needs (the gateway address and the key version), so a
// revision is self-contained.
type AISettings struct {
	Enabled bool `json:"enabled"`
	// KeyVersion is the version of the managed secret AIKeySecret the app
	// waits for (0 when disabled).
	KeyVersion int64 `json:"key_version"`
	// KeyID is the cluster key the secret was sealed to.
	KeyID string `json:"key_id,omitempty"`
	// BaseURL is the gateway as apps reach it, without "/v1".
	BaseURL string `json:"base_url,omitempty"`
	// Services use the AI (MS-4); empty: the whole app.
	Services Scope `json:"services,omitempty"`
}

// Equal says a and o are the same AI settings (services as a set).
func (a AISettings) Equal(o AISettings) bool {
	return a.Enabled == o.Enabled && a.KeyVersion == o.KeyVersion && a.KeyID == o.KeyID && a.BaseURL == o.BaseURL &&
		a.Services.Equal(o.Services)
}

// DesiredAI is the AI connection as the cluster receives it.
type DesiredAI struct {
	BaseURL   string   `json:"base_url"`
	KeySecret string   `json:"key_secret"`
	Services  []string `json:"services,omitempty"`
}

// EnableAI turns the app's AI on (or moves it to a newer key): the sealed
// key goes to the cluster and the settings record the version, in one
// transaction.
type EnableAI struct {
	// Version is the version of AIKeySecret the key was sealed for, KeyID the
	// cluster key it was sealed to, BaseURL the gateway as apps reach it.
	Version int64
	KeyID   string
	BaseURL string
	Sealed  []byte
	// Services use it (nil: as the AI in force says; the whole app when it
	// was off).
	Services *Scope
	// RequestedBy is the person who asked.
	RequestedBy identity.UserID
}

func (in EnableAI) Validate() error {
	switch {
	case in.Version < 1:
		return errx.InvalidField("version", "version must be positive")
	case in.KeyID == "" || len(in.KeyID) > 64:
		return errx.InvalidField("key_id", "key_id is required")
	case in.BaseURL == "" || len(in.BaseURL) > 2000:
		return errx.InvalidField("base_url", "base_url is required")
	case len(in.Sealed) == 0:
		return errx.InvalidField("sealed", "sealed is required")
	}
	return scopeNames(in.Services)
}

// Settings are the AI settings the input asks for, over current (the AI in
// force: its services stay unless the input names others).
func (in EnableAI) Settings(current AISettings) AISettings {
	services := Kept(in.Services, nil)
	if in.Services == nil && current.Enabled {
		services = current.Services.Normalize()
	}
	return AISettings{Enabled: true, KeyVersion: in.Version, KeyID: in.KeyID, BaseURL: in.BaseURL, Services: services}
}

// ScopeAI changes which services use the AI already on, without a new key.
type ScopeAI struct {
	Services    Scope
	RequestedBy identity.UserID
}

func (in ScopeAI) Validate() error { return in.Services.CheckNames("services") }

// DisableAI turns the app's AI off.
type DisableAI struct {
	RequestedBy identity.UserID
}

// AITarget is what the ai domain asks of an environment before it makes a
// key: whether its cluster can run AI, where to seal the key, which version
// the key takes, and the AI in force.
type AITarget struct {
	// Capable: the environment's cluster lists the AI capability.
	Capable bool `json:"capable"`
	// Key is the cluster key (with the app's name) a value is sealed to; nil
	// when the cluster cannot take managed secrets.
	Key *ClusterKey `json:"key"`
	// Revoked: the environment's cluster was revoked.
	Revoked bool `json:"revoked"`
	// NextVersion is the first version of AIKeySecret the cluster accepts.
	NextVersion int64 `json:"next_version"`
	// Revision is the latest settings revision.
	Revision int64 `json:"revision"`
	// Current is the AI of the settings in force.
	Current AISettings `json:"current"`
	// ScopedSettings: the cluster can give the AI to some services only.
	ScopedSettings bool `json:"scoped_settings"`
}

// WithAI is the next settings with AI as in. The AI is refused a variable
// the app already defines (a plain variable, a secret, a resource's or an
// open request) for a service the AI reaches: it would override it.
// changed is false when it already was so.
func (s Settings) WithAI(in AISettings, actor identity.UserID, open []SecretRequest) (next Settings, changed bool, err error) {
	in.Services = in.Services.Normalize()
	if s.AI.Equal(in) {
		return s, false, nil
	}
	if in.Enabled {
		held := s.holdings(open)
		for _, v := range AIVariables {
			if h := clash(held[v], in.Services, heldByAI); h != nil {
				return Settings{}, false, ErrVariableTaken(v, h.holder)
			}
		}
	}
	n := s.next(actor)
	n.AI = in
	return n.Normalize(), true, nil
}

// WithoutAI is the next settings with AI off; changed is false when it was.
func (s Settings) WithoutAI(actor identity.UserID) (next Settings, changed bool) {
	if !s.AI.Enabled {
		return s, false
	}
	n := s.next(actor)
	n.AI = AISettings{}
	return n.Normalize(), true
}

// AINextVersion is the version a key made now takes: above anything the
// managed secret AIKeySecret ever had here (the cluster takes only newer
// versions), whether it was a secret of the settings or the AI's own.
func AINextVersion(s Settings, versions map[string]int64) int64 {
	v := versions[AIKeySecret]
	if s.AI.KeyVersion > v {
		v = s.AI.KeyVersion
	}
	return v + 1
}

// AI is what the ai domain asks release, through an adapter (airelease):
// release records, in each settings revision, that the app uses the AI, and
// hands the key to the cluster in the same transaction. Implemented by
// releasesvc.Service.
type AI interface {
	// AITarget says what the environment's cluster offers for AI and which
	// version a key made now takes. It changes nothing.
	AITarget(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID) (AITarget, error)
	// EnableAI hands the sealed key to the cluster and turns the app's AI on
	// (or moves it to the key's version) in a new settings revision; a
	// config deployment applies it when something is online. Asking for what
	// is already in force changes nothing (a retry after a crash). A name
	// the app already defines is refused (409 VARIABLE_TAKEN).
	EnableAI(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input EnableAI) (SettingsChange, error)
	// DisableAI turns the app's AI off in a new settings revision.
	DisableAI(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input DisableAI) (SettingsChange, error)
	// ScopeAI changes which services use the AI that is on, keeping its key
	// (409 AI_NOT_ENABLED when it is off). The caller judged it.
	ScopeAI(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, input ScopeAI) (SettingsChange, error)
	// AIReach checks the services that would use the AI (nil: the AI in
	// force's, the whole app when it is off) against the app and its
	// cluster, and returns the ones it reaches as IT's rules see them. A
	// variable the app defines for one of them is refused (409
	// VARIABLE_TAKEN): the AI would override it.
	AIReach(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, services *Scope) ([]gate.Service, error)
	// AISettled says whether the app runs with settings revision (or a newer
	// one): the cluster was told it and no deployment is in progress, or
	// nothing is online to run anything. Only then is a key an earlier
	// revision named safe to revoke.
	AISettled(ctx context.Context, organization identity.OrganizationID, project project.ID, environment project.EnvironmentID, revision int64) (bool, error)
}

// AIChanged v1: the app's use of the AI gateway was switched on, off or moved
// to another key version. Settings is the new revision; Deployment the config
// deployment that applies it (null when another one will, or nothing is
// online yet). Never the key.
type AIChanged struct {
	Project     string        `json:"project_id"`
	Environment string        `json:"environment_id"`
	Revision    int64         `json:"revision"`
	Enabled     bool          `json:"enabled"`
	KeyVersion  int64         `json:"key_version"`
	Deployment  *DeploymentID `json:"deployment_id"`
	// Services use it (MS-4, additive); absent: the whole app.
	Services []string `json:"services,omitempty"`
}

func NewAIChanged(org identity.OrganizationID, actor identity.UserID, projectID string, now Settings, deployment *DeploymentID, n Names) event.Event {
	summary := "Activó la IA" + now.AI.Services.Label() + " de " + n.where()
	if !now.AI.Enabled {
		summary = "Desactivó la IA de " + n.where()
	}
	return event.New(EventAIChanged, 1, org, actor, event.Subject(AccessSubjectKind, now.Environment), summary,
		AIChanged{Project: projectID, Environment: now.Environment.String(), Revision: now.Revision, Enabled: now.AI.Enabled,
			KeyVersion: now.AI.KeyVersion, Deployment: deployment, Services: now.AI.Services})
}
