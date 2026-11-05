package resource

// What resource asks IT to approve, as the payload of the approval request
// (see release/approval.go): the approvalresource adapter decodes it to run
// the action again, as the requester, once it is approved. Payloads are
// deterministic: the same value is offered back to redeem the approval.

// The "op" every payload names.
const (
	OpDelete  = "resource.delete"
	OpConnect = "resource.connect"
	OpCreate  = "resource.create" // a create that connects (judged before provisioning)
)

// DeleteApproval is the payload of a held delete_resource.
type DeleteApproval struct {
	Op   string `json:"op"`
	Name string `json:"name"`
}

// ConnectApproval is the payload of a held connect_resource.
type ConnectApproval struct {
	Op     string `json:"op"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// CreateApproval is the payload of a held create that connects.
type CreateApproval struct {
	Op        string `json:"op"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	StorageGi int    `json:"storage_gi"`
	Prefix    string `json:"prefix"`
}
