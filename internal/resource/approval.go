package resource

import (
	"encoding/json"
	"maps"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

// What resource asks IT to approve, as the payload of the approval request
// (see release/approval.go): the approvalresource adapter decodes it to run
// the action again, as the requester, once it is approved. Payloads are
// deterministic: the same value is offered back to redeem the approval.

// The "op" every payload names.
const (
	OpDelete  = "resource.delete"
	OpConnect = "resource.connect"
	// OpCreate: a held create_resource (connecting it or not).
	OpCreate = "resource.create"
	// OpChange: a held change_resource.
	OpChange = "resource.change"
)

// PayloadVersion is the schema_version of the create and change payloads.
// Version 1 (a create that connected, before the catalog) carried no recipe:
// what it would create is no longer what was approved, so it is not run
// (ErrOldPayload).
const PayloadVersion = 2

// DeleteApproval is the payload of a held delete_resource.
type DeleteApproval struct {
	Op   string `json:"op"`
	Name string `json:"name"`
}

// ConnectApproval is the payload of a held connect_resource. Services are
// the ones asked for (absent: as the connection says; MS-4).
type ConnectApproval struct {
	Op       string    `json:"op"`
	Name     string    `json:"name"`
	Prefix   string    `json:"prefix"`
	Services *[]string `json:"services,omitempty"`
}

// CreateApproval is the payload of a held create_resource: the steps, each
// pinned to what the catalog resolved (recipe@version and every
// parameter), so what runs once approved is exactly what IT saw. One step
// today; a template creates several in one request (docs/catalog.md).
type CreateApproval struct {
	Op            string       `json:"op"`
	SchemaVersion int          `json:"schema_version"`
	Steps         []CreateStep `json:"steps"`
}

// CreateStep is one resource a held create makes.
type CreateStep struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Recipe        string         `json:"recipe"`
	RecipeVersion int            `json:"recipe_version"`
	Params        map[string]int `json:"params"`
	Connect       *CreateConnect `json:"connect,omitempty"`
}

// CreateConnect is how a held create connects its resource.
type CreateConnect struct {
	Prefix   string    `json:"prefix"`
	Services *[]string `json:"services,omitempty"`
}

// ChangeApproval is the payload of a held change_resource: the parameters
// asked for (only those), on the version the resource is pinned to.
type ChangeApproval struct {
	Op            string         `json:"op"`
	SchemaVersion int            `json:"schema_version"`
	Name          string         `json:"name"`
	Recipe        string         `json:"recipe"`
	RecipeVersion int            `json:"recipe_version"`
	Params        map[string]int `json:"params"`
}

// CodeOldApproval: an approved request written before the catalog cannot be
// run as it was approved.
const CodeOldApproval = "APPROVAL_OUTDATED"

// ErrOldPayload is the refusal to run an approved request of an older
// payload version.
func ErrOldPayload() error {
	return errx.Business("Esta aprobación es de antes del catálogo de recursos y ya no describe lo que se crearía. Pídelo de nuevo.").
		WithCode(CodeOldApproval)
}

// DecodeCreate reads a held create's payload (ErrOldPayload for version 1).
func DecodeCreate(raw json.RawMessage) (CreateApproval, error) {
	var p CreateApproval
	if err := json.Unmarshal(raw, &p); err != nil {
		return CreateApproval{}, err
	}
	if p.SchemaVersion != PayloadVersion || len(p.Steps) == 0 {
		return CreateApproval{}, ErrOldPayload()
	}
	return p, nil
}

// DecodeChange reads a held change's payload.
func DecodeChange(raw json.RawMessage) (ChangeApproval, error) {
	var p ChangeApproval
	if err := json.Unmarshal(raw, &p); err != nil {
		return ChangeApproval{}, err
	}
	if p.SchemaVersion != PayloadVersion {
		return ChangeApproval{}, ErrOldPayload()
	}
	return p, nil
}

// Create is the request the step re-runs, as requested by.
func (s CreateStep) Create() Create {
	in := Create{Name: s.Name, Kind: resourcekind.Kind(s.Kind), Recipe: s.Recipe, RecipeVersion: s.RecipeVersion, Params: maps.Clone(s.Params)}
	if s.Connect != nil {
		in.Connect = &ConnectOptions{Prefix: s.Connect.Prefix, Services: s.Connect.Services}
	}
	return in
}
