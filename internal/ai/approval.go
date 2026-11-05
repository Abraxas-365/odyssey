package ai

// What ai asks IT to approve, as the payload of the approval request: the
// approvalai adapter decodes it to run the action again, as the requester,
// once it is approved. Deterministic: offered back to redeem the approval.

// OpEnable is the "op" of the payload.
const OpEnable = "ai.enable"

// EnableApproval is the payload of a held enable_ai.
type EnableApproval struct {
	Op string `json:"op"`
}
