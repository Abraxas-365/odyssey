package ai

// What ai asks IT to approve, as the payload of the approval request: the
// approvalai adapter decodes it to run the action again, as the requester,
// once it is approved. Deterministic: offered back to redeem the approval.

// OpEnable is the "op" of the payload.
const OpEnable = "ai.enable"

// EnableApproval is the payload of a held enable_ai. Services are the ones
// asked for (absent: the ones in force, the whole app when it is off; MS-4).
type EnableApproval struct {
	Op       string    `json:"op"`
	Services *[]string `json:"services,omitempty"`
}
