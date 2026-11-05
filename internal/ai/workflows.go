package ai

import "github.com/Abraxas-365/odyssey/internal/project"

// Workflows of the AI (aisvc). Enable and rotate run the same steps; every
// step can run twice with the same effect.
//
//	ai.enable / ai.rotate:
//	  create_key  make the key at the gateway (after revoking a leftover of the
//	              same name) and seal it to the cluster's key; the output is
//	              the sealed bytes, never the key
//	  apply       one transaction: hand the sealed key to the cluster, record
//	              the AI in the app's settings, make the key active
//	  settle      wait until the app runs with the revision
//	  revoke_old  revoke the keys the new one replaced and drop their rows
//
//	ai.disable (the settings are changed by the request, in its transaction):
//	  settle      wait until the app runs without the AI
//	  revoke      revoke every key of the environment at the gateway, drop the rows
const (
	WorkflowEnable  = "ai.enable"
	WorkflowRotate  = "ai.rotate"
	WorkflowDisable = "ai.disable"
)

// Steps.
const (
	StepCreateKey = "create_key"
	StepApply     = "apply"
	StepSettle    = "settle"
	StepRevokeOld = "revoke_old"
	StepRevoke    = "revoke"
)

// Input is the input of the three workflows. Key is the row the run works on
// (a disable works on every row of the environment and only names its run);
// Revision is the settings revision the run waits for (disable only).
type Input struct {
	Key         KeyID                 `json:"key_id"`
	Project     project.ID            `json:"project_id"`
	Environment project.EnvironmentID `json:"environment_id"`
	Revision    int64                 `json:"revision,omitempty"`
	// Keys are the rows a disable revokes (the ones it marked): it never
	// touches a key made after it, nor one a later rotation retired, which the
	// app may still be running with.
	Keys []KeyID `json:"key_ids,omitempty"`
	// Gone: the environment (or its project) no longer exists, so no app runs
	// with the keys and there is nothing to wait for or to update; the run only
	// revokes them at the gateway (the sweep sets it, never a request).
	Gone bool `json:"gone,omitempty"`
}
