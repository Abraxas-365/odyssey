package release

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

// Variables and secrets of an environment (2a). Plain variables live in
// Settings.Env; secret variables are references in Settings.Secrets. A
// secret's value is never stored by Odicea: a managed one is sealed in the
// browser to the cluster's key and only travels (fleet keeps the sealed
// bytes until the agent acknowledges them); an external one is read by the
// cluster's External Secrets Operator from the company's vault.

// SecretSource says where a secret's value comes from.
type SecretSource string

const (
	SecretManaged  SecretSource = "managed"  // a person loaded it in the console
	SecretExternal SecretSource = "external" // the company's vault, through ESO
)

func (s SecretSource) Valid() bool { return s == SecretManaged || s == SecretExternal }

// SecretRef is one secret variable: which value, never the value.
type SecretRef struct {
	Name   string       `json:"name"`
	Source SecretSource `json:"source"`
	// Version of a managed value: 1 for the first load, +1 per rotation.
	Version int64 `json:"version,omitempty"`
	// KeyID is the cluster key the managed value was sealed to: when the
	// environment's cluster has another key (it moved, or its agent was
	// reinstalled) the value cannot be opened there and must be loaded again.
	KeyID    string          `json:"key_id,omitempty"`
	LoadedBy identity.UserID `json:"loaded_by,omitzero"`
	LoadedAt *time.Time      `json:"loaded_at,omitempty"`
	// Store, Key and Property locate an external value: the cluster's
	// ClusterSecretStore, the key in the vault and (optional) the property.
	Store    string `json:"store,omitempty"`
	Key      string `json:"key,omitempty"`
	Property string `json:"property,omitempty"`
	// Services read it (MS-4); empty: the whole app.
	Services Scope `json:"services,omitempty"`
}

// SecretRequest is a managed secret someone asked for (an assistant's
// request_secret, or the console) whose value nobody loaded yet. It is not
// part of the settings: until a value exists nothing changes in the cluster.
type SecretRequest struct {
	Environment  project.EnvironmentID   `json:"-" db:"environment_id"`
	Organization identity.OrganizationID `json:"-" db:"organization_id"`
	Name         string                  `json:"name" db:"name"`
	// Note says what the value is for, in the requester's words.
	Note         string          `json:"note" db:"note"`
	RequestedBy  identity.UserID `json:"requested_by" db:"requested_by"`
	RequestedVia string          `json:"requested_via" db:"requested_via"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
	// Services will read it (MS-4); empty: the whole app. Loading the value
	// keeps it.
	Services Scope `json:"services,omitempty" db:"-"`
}

// Limits of variables and secrets.
const (
	// MaxSettingsVars bounds an environment's variables, secrets and open
	// secret requests together (Settings.NameCount).
	MaxSettingsVars = 100
	MaxSecretNote   = 200
	MaxSecretStore  = 253
	MaxSecretKey    = 512
	MaxSecretProp   = 256
	// PlatformEnvPrefix is reserved for what Odicea injects (ODICEA_AUTH_…).
	PlatformEnvPrefix = "ODICEA_"
)

// Codes of variables and secrets.
const (
	// CodeSecretsUnavailable: the cluster cannot take managed secrets (its
	// agent announced no secret key).
	CodeSecretsUnavailable = "SECRETS_UNAVAILABLE"
	// CodeExternalSecretsUnavailable: the cluster has no External Secrets Operator.
	CodeExternalSecretsUnavailable = "EXTERNAL_SECRETS_UNAVAILABLE"
	// CodeSecretKeyChanged: the value was sealed to a key the cluster no
	// longer has (reload the settings page and type it again).
	CodeSecretKeyChanged = "SECRET_KEY_CHANGED"
	// CodeSecretVersionConflict: someone else loaded the secret meanwhile.
	CodeSecretVersionConflict = "SECRET_VERSION_CONFLICT"
)

var storePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// validVarName is the rule every variable and secret name follows.
func validVarName(field, name string) error {
	switch {
	case !envKeyPattern.MatchString(name):
		return errx.InvalidField(field, "names are letters, digits and _, not starting with a digit (up to 128): "+name)
	case reservedName(name):
		return errx.InvalidField(field, "names starting with ODYSSEY_ or ODICEA_ are set by the platform: "+name)
	}
	return nil
}

func reservedName(name string) bool {
	up := strings.ToUpper(name)
	return strings.HasPrefix(up, ReservedEnvPrefix) || strings.HasPrefix(up, PlatformEnvPrefix)
}

func validVarValue(field, name, value string) error {
	if len(value) > MaxEnvValue || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return errx.InvalidField(field, "values are up to 4096 bytes of text without NUL: "+name)
	}
	return nil
}

// SetEnv changes plain variables: Set adds or replaces, Unset removes.
// Services says in which layer: empty, the whole app's; else the same
// variables go to (or leave) the own layer of each service named (MS-4). An
// Unset for the whole app removes the variable from every layer.
type SetEnv struct {
	Set         map[string]string `json:"set"`
	Unset       []string          `json:"unset"`
	Services    Scope             `json:"services"`
	RequestedBy identity.UserID   `json:"-"`
}

func (in SetEnv) Validate() error {
	if len(in.Set)+len(in.Unset) == 0 {
		return errx.InvalidField("set", "set or unset at least one variable")
	}
	if len(in.Set) > MaxSettingsVars || len(in.Unset) > MaxSettingsVars {
		return errx.InvalidField("set", "at most 100 variables per change")
	}
	if err := in.Services.CheckNames("services"); err != nil {
		return err
	}
	for k, v := range in.Set {
		if err := validVarName("set", k); err != nil {
			return err
		}
		if err := validVarValue("set", k, v); err != nil {
			return err
		}
	}
	for _, k := range in.Unset {
		if err := validVarName("unset", k); err != nil {
			return err
		}
		if _, both := in.Set[k]; both {
			return errx.InvalidField("unset", "a variable cannot be set and unset at once: "+k)
		}
	}
	return nil
}

// RequestSecret asks a person to load a managed secret's value. Services is
// who will read it (nil: the whole app, or, asked again, what was asked).
type RequestSecret struct {
	Name         string          `json:"name"`
	Note         string          `json:"note"`
	Services     *Scope          `json:"services"`
	RequestedBy  identity.UserID `json:"-"`
	RequestedVia string          `json:"-"`
}

func (in RequestSecret) Normalize() RequestSecret {
	in.Name, in.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Note)
	return in
}

func (in RequestSecret) Validate() error {
	if err := validVarName("name", in.Name); err != nil {
		return err
	}
	if utf8.RuneCountInString(in.Note) > MaxSecretNote || !utf8.ValidString(in.Note) || strings.ContainsRune(in.Note, 0) {
		return errx.InvalidField("note", "note is up to 200 characters of text")
	}
	return scopeNames(in.Services)
}

// scopeNames validates the names of an optional scope.
func scopeNames(s *Scope) error {
	if s == nil {
		return nil
	}
	return s.CheckNames("services")
}

// LoadSecret stores a managed secret's value, sealed in the browser to the
// environment's cluster key for this name and Version (pkg/secretbox): the
// first load is version 1, each rotation the next one. A plain variable of
// the same name is removed (it becomes a secret). Services is who reads it
// (nil: as the secret, or its request, already says; the whole app for a
// new one).
type LoadSecret struct {
	Name        string          `json:"-"`
	Version     int64           `json:"version"`
	KeyID       string          `json:"key_id"`
	Sealed      []byte          `json:"sealed"`
	Services    *Scope          `json:"services"`
	RequestedBy identity.UserID `json:"-"`
	// MayChangeExternal says the caller holds odyssey:secrets:external (the
	// handler fills it in): only then may a value replace a secret IT pointed
	// at the vault.
	MayChangeExternal bool `json:"-"`
}

func (in LoadSecret) Validate() error {
	if err := validVarName("name", in.Name); err != nil {
		return err
	}
	switch {
	case in.Version < 1:
		return errx.InvalidField("version", "version must be positive")
	case in.KeyID == "" || len(in.KeyID) > 64:
		return errx.InvalidField("key_id", "key_id is required")
	case len(in.Sealed) < secretbox.MinSealed || len(in.Sealed) > secretbox.MaxSealed:
		return errx.InvalidField("sealed", "sealed must be the value sealed to the cluster key (base64), up to 64 KB of value")
	}
	return scopeNames(in.Services)
}

// SetExternalSecret points a secret variable at the company's vault.
// Services: as in LoadSecret.
type SetExternalSecret struct {
	Name        string          `json:"-"`
	Store       string          `json:"store"`
	Key         string          `json:"key"`
	Property    string          `json:"property"`
	Services    *Scope          `json:"services"`
	RequestedBy identity.UserID `json:"-"`
}

func (in SetExternalSecret) Normalize() SetExternalSecret {
	in.Store, in.Key, in.Property = strings.TrimSpace(in.Store), strings.TrimSpace(in.Key), strings.TrimSpace(in.Property)
	return in
}

func (in SetExternalSecret) Validate() error {
	if err := validVarName("name", in.Name); err != nil {
		return err
	}
	switch {
	case len(in.Store) > MaxSecretStore || !storePattern.MatchString(in.Store):
		return errx.InvalidField("store", "store is the name of a ClusterSecretStore of the cluster")
	case in.Key == "" || len(in.Key) > MaxSecretKey || !printable(in.Key):
		return errx.InvalidField("key", "key is the secret's key in the vault (up to 512 characters)")
	case len(in.Property) > MaxSecretProp || !printable(in.Property):
		return errx.InvalidField("property", "property is up to 256 characters")
	}
	return scopeNames(in.Services)
}

// ScopeSecret changes who reads a secret (or an open request) without
// touching its value.
type ScopeSecret struct {
	Name        string          `json:"-"`
	Services    Scope           `json:"services"`
	RequestedBy identity.UserID `json:"-"`
	// MayChangeExternal: as in LoadSecret.
	MayChangeExternal bool `json:"-"`
}

func (in ScopeSecret) Validate() error {
	if err := validVarName("name", in.Name); err != nil {
		return err
	}
	return in.Services.CheckNames("services")
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// RemoveSecret drops a secret variable, or a request nobody answered.
type RemoveSecret struct {
	Name        string          `json:"-"`
	RequestedBy identity.UserID `json:"-"`
	// MayChangeExternal: as in LoadSecret; removing an external secret
	// needs it.
	MayChangeExternal bool `json:"-"`
}

// ErrExternalSecretForbidden is a change to a secret IT pointed at the
// company's vault by someone without odyssey:secrets:external.
func ErrExternalSecretForbidden(name string) error {
	return errx.Forbidden(name + " is read from the company's vault: changing it needs the permission odyssey:secrets:external (ask IT)")
}

func (in RemoveSecret) Validate() error { return validVarName("name", in.Name) }

// ClusterSecrets is what the environment's cluster offers for secrets (an
// environment not placed yet is judged by the organization's default
// cluster, like access).
type ClusterSecrets struct {
	// Key is where a managed value is sealed to (nil: the cluster cannot
	// take managed secrets).
	Key *ClusterKey `json:"key"`
	// External is true when the cluster has the External Secrets Operator.
	External bool `json:"external"`
	// Stores are its ClusterSecretStores, by name.
	Stores []string `json:"stores"`
	// Revoked: the environment's cluster was revoked; it offers nothing
	// until IT moves the environment.
	Revoked bool `json:"revoked"`
	// AI: the cluster's agent lists the AI capability (odicea.ai).
	AI bool `json:"ai"`
	// ScopedSettings: the cluster's agent can give a setting to some
	// services only (odicea.service-settings, MS-4).
	ScopedSettings bool `json:"scoped_settings"`
	// Failures are the managed values whose last delivery did not arrive
	// (by name; not part of the API: SecretView carries them).
	Failures map[string]SecretFailure `json:"-"`
}

// SecretFailure is why the last value of a managed secret did not reach the
// cluster.
type SecretFailure struct {
	// App is the app the value was for (the view only counts its own).
	App      string    `json:"-"`
	Version  int64     `json:"version"`
	Error    string    `json:"error"`
	FailedAt time.Time `json:"failed_at"`
}

// ClusterKey is a cluster's public key for sealing (pkg/secretbox), with the
// app a value sealed now is bound to (the service fills it in: the cluster
// does not know the app).
type ClusterKey struct {
	Cluster string `json:"cluster_id"`
	KeyID   string `json:"key_id"`
	Public  []byte `json:"public_key"`
	App     string `json:"app"`
}

// SecretDelivery is a sealed value on its way to the cluster.
type SecretDelivery struct {
	App     string
	Name    string
	Version int64
	KeyID   string
	Sealed  []byte
	By      identity.UserID
}

// DesiredSecret is one secret variable as the cluster applies it.
type DesiredSecret struct {
	Name     string       `json:"name"`
	Source   SecretSource `json:"source"`
	Version  int64        `json:"version,omitempty"`
	KeyID    string       `json:"key_id,omitempty"`
	Store    string       `json:"store,omitempty"`
	Key      string       `json:"key,omitempty"`
	Property string       `json:"property,omitempty"`
	// Services read it (empty: every service).
	Services []string `json:"services,omitempty"`
}

// WithSettings is d configured by s: the environment's variables over the
// release's, and its secrets (a secret wins over a variable of its name).
// Each setting keeps the services it reaches; a scoped one (MS-4) does not
// remove the release's variable of its name, since the services it does not
// reach keep it (the agent decides per workload what wins). A service's own
// variables go with the service, if the version has it.
func (d DesiredApp) WithSettings(s Settings) DesiredApp {
	s = s.Normalize()
	d.Access = string(s.Access.Mode)
	env := make(map[string]string, len(d.Env)+len(s.Env))
	for k, v := range d.Env {
		env[k] = v
	}
	for k, v := range s.Env {
		env[k] = v
	}
	d.ServiceEnv = make(map[string]map[string]string, len(d.Services))
	for _, svc := range d.Services {
		if env := s.ServiceEnv[svc.Name]; len(env) > 0 {
			d.ServiceEnv[svc.Name] = maps.Clone(env)
		}
	}
	d.Resources = make([]DesiredResource, 0, len(s.Resources))
	for _, r := range s.Resources {
		if r.Services.Whole() {
			for _, v := range resourcekind.Variables(r.Kind, r.Prefix) {
				delete(env, v) // the connection wins over a release's variable of its name
			}
		}
		d.Resources = append(d.Resources, DesiredResource{Name: r.Name, Kind: r.Kind, Prefix: r.Prefix, Services: r.Services})
	}
	d.Secrets = make([]DesiredSecret, 0, len(s.Secrets))
	for _, r := range s.Secrets {
		if r.Services.Whole() {
			delete(env, r.Name)
		}
		d.Secrets = append(d.Secrets, DesiredSecret{Name: r.Name, Source: r.Source, Version: r.Version, KeyID: r.KeyID,
			Store: r.Store, Key: r.Key, Property: r.Property, Services: r.Services})
	}
	d.AI = nil
	if s.AI.Enabled {
		if s.AI.Services.Whole() {
			for _, v := range AIVariables {
				delete(env, v) // the AI wins over a release's variable of its name
			}
		}
		d.AI = &DesiredAI{BaseURL: s.AI.BaseURL, KeySecret: AIKeySecret, Services: s.AI.Services}
		d.Secrets = append(d.Secrets, DesiredSecret{Name: AIKeySecret, Source: SecretManaged, Version: s.AI.KeyVersion, KeyID: s.AI.KeyID,
			Services: s.AI.Services})
	}
	d.Env = env
	return d
}

// SecretState is how a secret variable stands against the environment's
// cluster, in the console's words.
type SecretState string

const (
	// SecretStateMissing: requested, nobody loaded a value yet ("Falta cargar").
	SecretStateMissing SecretState = "missing"
	// SecretStateLoaded: a managed value is in force.
	SecretStateLoaded SecretState = "loaded"
	// SecretStateReload: the value was sealed to a key the cluster no longer
	// has (it moved, or its agent was reinstalled): load it again.
	SecretStateReload SecretState = "reload"
	// SecretStateExternal: read from the company's vault.
	SecretStateExternal SecretState = "external"
	// SecretStateUnavailable: an external secret whose store (or operator)
	// the cluster does not have.
	SecretStateUnavailable SecretState = "unavailable"
	// SecretStateFailed: the value in force never reached the cluster (it
	// could not store it, or it was offline past the delivery window): load
	// it again. SecretView.Failure says why.
	SecretStateFailed SecretState = "failed"
)

// SecretView is one secret variable (or request) as the console shows it.
type SecretView struct {
	SecretRef
	State SecretState `json:"state"`
	// NextVersion is the version a value loaded now must be sealed for: one
	// more than the highest this name ever had in the environment, even if
	// the secret was removed since (the cluster may still hold that value,
	// and it only takes newer versions).
	NextVersion int64 `json:"next_version"`
	// Failure is why the value in force did not arrive (state failed).
	Failure *SecretFailure `json:"failure,omitempty"`
	// Request is the open request (state missing).
	Request *SecretRequest `json:"request,omitempty"`
}

// Secrets are an environment's secret variables and open requests, sorted
// by name, and what its cluster offers.
type Secrets struct {
	Items   []SecretView   `json:"items"`
	Cluster ClusterSecrets `json:"cluster"`
}

// ViewSecrets judges every secret of s against what the cluster offers; the
// open requests of names that are not secrets yet come as missing. versions
// are the environment's high-water marks (Repository.ListSecretVersions).
func ViewSecrets(s Settings, requests []SecretRequest, c ClusterSecrets, versions map[string]int64) Secrets {
	s = s.Normalize()
	if c.Stores == nil {
		c.Stores = []string{}
	}
	out := Secrets{Items: make([]SecretView, 0, len(s.Secrets)+len(requests)), Cluster: c}
	for _, r := range s.Secrets {
		v := SecretView{SecretRef: r, State: secretState(r, c), NextVersion: NextSecretVersion(s, versions, r.Name)}
		if f, ok := c.Failures[r.Name]; ok && r.Source == SecretManaged && f.Version == r.Version && v.State == SecretStateLoaded {
			v.State, v.Failure = SecretStateFailed, &f
		}
		out.Items = append(out.Items, v)
	}
	for i := range requests {
		if _, ok := s.Secret(requests[i].Name); ok {
			continue
		}
		req := requests[i]
		out.Items = append(out.Items, SecretView{SecretRef: SecretRef{Name: req.Name, Source: SecretManaged, Services: req.Services},
			State: SecretStateMissing, NextVersion: NextSecretVersion(s, versions, req.Name), Request: &req})
	}
	slices.SortFunc(out.Items, func(a, b SecretView) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// NextSecretVersion is the version the next value of name must carry: one
// more than both its current reference and its high-water mark.
func NextSecretVersion(s Settings, versions map[string]int64, name string) int64 {
	v := versions[name]
	if r, ok := s.Secret(name); ok && r.Source == SecretManaged && r.Version > v {
		v = r.Version
	}
	return v + 1
}

func secretState(r SecretRef, c ClusterSecrets) SecretState {
	if r.Source == SecretExternal {
		if c.External && slices.Contains(c.Stores, r.Store) {
			return SecretStateExternal
		}
		return SecretStateUnavailable
	}
	if c.Key == nil || c.Key.KeyID != r.KeyID {
		return SecretStateReload
	}
	return SecretStateLoaded
}

// NameCount is how many names an environment holds under s: variables,
// secrets, the variables of connected resources and of the AI, and open
// requests, each name once (a request whose name is a secret or a variable
// already takes no second place); a service's own variable takes a place of
// its own (it is one more value).
func (s Settings) NameCount(open []SecretRequest) int {
	n := 0
	for _, held := range s.holdings(open) {
		shared := false
		for _, h := range held {
			switch {
			case h.layer != "":
				n++
			case !shared:
				shared = true
				n++
			}
		}
	}
	return n
}

// ErrTooManyVars: an environment's names (see NameCount) pass MaxSettingsVars.
func ErrTooManyVars(field string) error {
	return errx.InvalidField(field, "an environment has at most 100 variables, secrets and pending requests")
}

// CheckRequest says whether a request for name, for the services of scope,
// fits: no resource or AI variable of its name reaches them, and an open
// request of that name is renewed while any other takes one more place.
func (s Settings) CheckRequest(open []SecretRequest, name string, scope Scope) error {
	if h := clash(s.holdings(nil)[name], scope, heldByVariable, heldBySecret); h != nil {
		return ErrVariableTaken(name, h.holder)
	}
	if slices.ContainsFunc(open, func(r SecretRequest) bool { return r.Name == name }) {
		return nil
	}
	if s.NameCount(open) >= MaxSettingsVars {
		return ErrTooManyVars("name")
	}
	return nil
}

// EnvChange is what WithEnv did: the next settings, the names set and unset
// (sorted) and what may surprise (SettingsChange.Warnings).
type EnvChange struct {
	Next     Settings
	Set      []string
	Unset    []string
	Warnings []string
}

// layer is the plain variables of service ("": the whole app's), made when
// missing.
func (s *Settings) layer(service string) map[string]string {
	if service == "" {
		return s.Env
	}
	if s.ServiceEnv[service] == nil {
		s.ServiceEnv[service] = map[string]string{}
	}
	return s.ServiceEnv[service]
}

// WithEnv is the next settings with in applied (in validated); open are the
// environment's open secret requests, which count against the limit. A name
// that is a secret for a service the variable would reach cannot become a
// plain variable (remove the secret first), and one a resource or the AI
// sets there is taken. A service's own variable wins over the whole app's.
func (s Settings) WithEnv(in SetEnv, open []SecretRequest) (EnvChange, error) {
	n := s.next(in.RequestedBy)
	held := s.holdings(open)
	layers := []string{""}
	if scope := in.Services.Normalize(); !scope.Whole() {
		layers = scope
	}
	set, unset := map[string]bool{}, map[string]bool{}
	var warnings []string
	for _, k := range slices.Sorted(maps.Keys(in.Set)) {
		v := in.Set[k]
		for _, layer := range layers {
			var scope Scope
			if layer != "" {
				scope = Scope{layer}
			}
			if h := clash(held[k], scope, heldByVariable, heldByRequest); h != nil {
				if h.kind == heldBySecret {
					return EnvChange{}, errx.InvalidField("set", k+" is a secret: remove the secret to make it a plain variable")
				}
				return EnvChange{}, ErrVariableTaken(k, h.holder)
			}
			env := n.layer(layer)
			if old, ok := env[k]; !ok || old != v {
				set[k] = true
			}
			env[k] = v
			warnings = append(warnings, shadowing(n, k, layer)...)
		}
	}
	for _, k := range in.Unset {
		if len(layers) == 1 && layers[0] == "" {
			if _, ok := n.Env[k]; ok {
				unset[k] = true
				delete(n.Env, k)
			}
			for _, env := range n.ServiceEnv {
				if _, ok := env[k]; ok {
					unset[k] = true
					delete(env, k)
				}
			}
			continue
		}
		for _, layer := range layers {
			if _, ok := n.ServiceEnv[layer][k]; ok {
				unset[k] = true
				delete(n.ServiceEnv[layer], k)
			}
		}
	}
	n = n.Normalize()
	if n.NameCount(open) > MaxSettingsVars {
		return EnvChange{}, ErrTooManyVars("set")
	}
	return EnvChange{Next: n, Set: slices.Sorted(maps.Keys(set)), Unset: slices.Sorted(maps.Keys(unset)), Warnings: slices.Compact(warnings)}, nil
}

// shadowing words what setting k in layer means for the other layer: a
// service's own value replaces the whole app's there, and a service with its
// own value keeps it when the whole app's changes.
func shadowing(n Settings, k, layer string) []string {
	if layer != "" {
		if _, ok := n.Env[k]; ok {
			return []string{"El servicio " + layer + " usa su propio valor de " + k + ", no el de la app."}
		}
		return nil
	}
	var out []string
	for _, svc := range slices.Sorted(maps.Keys(n.ServiceEnv)) {
		if _, ok := n.ServiceEnv[svc][k]; ok {
			out = append(out, "El servicio "+svc+" conserva su propio valor de "+k+".")
		}
	}
	return out
}

// WithSecret is the next settings with ref in place of the secret of its
// name, for the services ref.Services reaches. A plain variable of its name
// there becomes the secret (removed), but one of the whole app cannot give
// way to a secret of some services only (the others would lose it). A name
// a resource or the AI sets for those services is taken. open are the open
// secret requests (the one of ref's name is answered by it and is not
// counted twice).
func (s Settings) WithSecret(ref SecretRef, actor identity.UserID, open []SecretRequest) (Settings, error) {
	ref.Services = ref.Services.Normalize()
	if h := clash(s.holdings(nil)[ref.Name], ref.Services, heldByVariable, heldBySecret); h != nil {
		return Settings{}, ErrVariableTaken(ref.Name, h.holder)
	}
	n := s.next(actor)
	if _, plain := n.Env[ref.Name]; plain {
		if !ref.Services.Whole() {
			return Settings{}, errx.InvalidField("services", ref.Name+" is a variable of the whole app: remove it, or give the secret to the whole app")
		}
		delete(n.Env, ref.Name)
	}
	for svc, env := range n.ServiceEnv {
		if ref.Services.Reaches(svc) {
			delete(env, ref.Name)
		}
	}
	n.Secrets = slices.DeleteFunc(n.Secrets, func(r SecretRef) bool { return r.Name == ref.Name })
	n.Secrets = append(n.Secrets, ref)
	n = n.Normalize()
	if n.NameCount(open) > MaxSettingsVars {
		return Settings{}, ErrTooManyVars("name")
	}
	return n, nil
}

// WithoutSecret is the next settings without the secret named name.
func (s Settings) WithoutSecret(name string, actor identity.UserID) Settings {
	n := s.next(actor)
	n.Secrets = slices.DeleteFunc(n.Secrets, func(r SecretRef) bool { return r.Name == name })
	return n
}
