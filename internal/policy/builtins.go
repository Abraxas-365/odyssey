package policy

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// Built-in rule keys.
const (
	BuiltinProductionDeploy = "production_deploy_approval"
	BuiltinPublicAccess     = "public_access_approval"
	BuiltinProductionVulns  = "production_critical_vulnerabilities"
	BuiltinSandboxPublic    = "sandbox_not_public"
)

// Builtins are the rules every organization is born with. They can be
// switched off, never changed or deleted. The seeding is lazy and idempotent
// (the first read of an organization's rulebook inserts the missing ones, by
// builtin key), so a key added here reaches existing organizations too.
// The IDs are generated per organization; the key is the stable identity.
func Builtins(organization identity.OrganizationID, now time.Time) []Rule {
	n := 0
	mk := func(key, name, description, expression string, effect Effect) Rule {
		k := key
		// one microsecond apart: the list keeps this order (the column has
		// microsecond precision)
		at := now.Add(time.Duration(n) * time.Microsecond)
		n++
		return Rule{ID: NewRuleID(), Organization: organization, Name: name, Description: description,
			Expression: expression, Effect: effect, Approvers: permission.ApprovalsDecide, Enabled: true,
			Builtin: &k, Version: 1, CreatedAt: at, UpdatedAt: at}
	}
	return []Rule{
		mk(BuiltinProductionDeploy, "Publicar en producción requiere aprobación de TI",
			"Publicar una app de nivel producción necesita que TI lo apruebe.",
			`action == 'deploy' && project.tier == 'production'`, EffectApproval),
		mk(BuiltinPublicAccess, "Hacer pública una app requiere aprobación de TI",
			"Dejar que cualquiera con el enlace abra una app necesita que TI lo apruebe.",
			`action == 'set_access' && access.mode == 'public'`, EffectApproval),
		mk(BuiltinProductionVulns, "Producción no publica con vulnerabilidades críticas",
			"Una app de producción no se publica si el escaneo encontró vulnerabilidades críticas.",
			`action == 'deploy' && project.tier == 'production' && scan.known && scan.critical > 0`, EffectDeny),
		mk(BuiltinSandboxPublic, "Sandbox no puede ser pública",
			"Una app de nivel sandbox nunca puede abrirse a cualquiera con el enlace.",
			`action == 'set_access' && project.tier == 'sandbox' && access.mode == 'public'`, EffectDeny),
	}
}
