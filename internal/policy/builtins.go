package policy

import (
	"time"

	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/permission"
)

// Built-in rule keys.
const (
	BuiltinProductionDeploy   = "production_deploy_approval"
	BuiltinPublicAccess       = "public_access_approval"
	BuiltinProductionVulns    = "production_critical_vulnerabilities"
	BuiltinSandboxPublic      = "sandbox_not_public"
	BuiltinRequiresScan       = "production_requires_scan"
	BuiltinProductionServices = "production_services_approval"
)

// Builtins are the rules every organization is born with. They can be
// switched on or off, never changed or deleted. Most are born on;
// production_requires_scan and production_services_approval are born OFF
// (they hold back actions of organizations that did not ask for it:
// enabling them is IT's decision, docs/policy.md). The seeding is lazy and idempotent
// (the first read of an organization's rulebook inserts the missing ones, by
// builtin key), so a key added here reaches existing organizations too (a
// rule born off stays off for them).
// The IDs are generated per organization; the key is the stable identity.
func Builtins(organization identity.OrganizationID, now time.Time) []Rule {
	n := 0
	mk := func(key, name, description, expression string, effect Effect, enabled bool) Rule {
		k := key
		// one microsecond apart: the list keeps this order (the column has
		// microsecond precision)
		at := now.Add(time.Duration(n) * time.Microsecond)
		n++
		return Rule{ID: NewRuleID(), Organization: organization, Name: name, Description: description,
			Expression: expression, Effect: effect, Approvers: permission.ApprovalsDecide, Enabled: enabled,
			Builtin: &k, Version: 1, CreatedAt: at, UpdatedAt: at}
	}
	return []Rule{
		mk(BuiltinProductionDeploy, "Publicar en producción requiere aprobación de TI",
			"Publicar una app de nivel producción necesita que TI lo apruebe.",
			`action == 'deploy' && project.tier == 'production'`, EffectApproval, true),
		mk(BuiltinPublicAccess, "Hacer pública una app requiere aprobación de TI",
			"Dejar que cualquiera con el enlace abra una app necesita que TI lo apruebe.",
			`action == 'set_access' && access.mode == 'public'`, EffectApproval, true),
		mk(BuiltinProductionVulns, "Producción no publica con vulnerabilidades críticas",
			"Una app de producción no se publica si el escaneo encontró vulnerabilidades críticas.",
			`action == 'deploy' && project.tier == 'production' && scan.known && scan.critical > 0`, EffectDeny, true),
		mk(BuiltinSandboxPublic, "Sandbox no puede ser pública",
			"Una app de nivel sandbox nunca puede abrirse a cualquiera con el enlace.",
			`action == 'set_access' && project.tier == 'sandbox' && access.mode == 'public'`, EffectDeny, true),
		mk(BuiltinRequiresScan, "Producción exige revisión de vulnerabilidades",
			"Si una app de producción no pudo revisarse en busca de vulnerabilidades (la revisión falló, no llegó a tiempo o el clúster no la hace), TI debe aprobar la publicación. Viene apagada.",
			`action == 'deploy' && project.tier == 'production' && scan.checked && !scan.known`, EffectApproval, false),
		mk(BuiltinProductionServices, "Cambiar los servicios de producción requiere aprobación de TI",
			"Agregar, quitar o mover un servicio de una app de producción (su carpeta o la dirección en la que responde) necesita que TI lo apruebe. Viene apagada.",
			`action == 'set_services' && project.tier == 'production'`, EffectApproval, false),
	}
}
