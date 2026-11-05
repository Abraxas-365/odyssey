package release

// What release asks IT to approve, as the payload of the approval request
// (the approval domain stores it as is; the approvalrelease adapter decodes
// it to run the action again when it is approved). Payloads are
// deterministic: the same value is offered back to redeem the approval.

// The "op" every payload names: which action of release to run again.
const (
	OpSetAccess       = "set_access"
	OpConnectResource = "connect_resource"
	// OpDeployment is a deployment held by its workflow's authorize step: it
	// is not re-run, the workflow continues (nothing executes it).
	OpDeployment = "deployment"
)

// SetAccessApproval is the payload of a held set_access.
type SetAccessApproval struct {
	Op   string     `json:"op"`
	Mode AccessMode `json:"mode"`
}

// ConnectResourceApproval is the payload of a held connect_resource.
type ConnectResourceApproval struct {
	Op     string `json:"op"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// DeploymentApproval is the payload of a held deployment (deploy or rollback).
type DeploymentApproval struct {
	Op         string       `json:"op"`
	Deployment DeploymentID `json:"deployment_id"`
	Reason     Reason       `json:"reason"`
}
