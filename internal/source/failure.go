package source

// Error codes of a push that was not published, and of refused requests.
const (
	CodeNotConfigured = "GITHUB_NOT_CONFIGURED"     // this Odyssey has no GitHub App
	CodeStateInvalid  = "GITHUB_STATE_INVALID"      // the setup callback's state or the claim is forged, expired or not the user's
	CodeNotYours      = "GITHUB_NOT_YOURS"          // the GitHub user does not own/administer the installation's account
	CodeTaken         = "GITHUB_INSTALLATION_TAKEN" // another organization linked it
	CodeNoAccess      = "GITHUB_NO_ACCESS"          // the installation does not reach the repository (anymore)
	CodeBranchMissing = "GITHUB_BRANCH_MISSING"     // the branch does not exist
	CodeRootMissing   = "SOURCE_ROOT_MISSING"       // the folder is not in the commit
	CodeTooLarge      = "SOURCE_TOO_LARGE"          // the commit exceeds the source limits
	CodeImportFailed  = "SOURCE_IMPORT_FAILED"      // downloading or repacking failed
	CodeProviderDown  = "GITHUB_UNAVAILABLE"        // the provider did not answer (retried)
	CodeDeployRefused = "DEPLOY_REFUSED"            // the deployment was refused (see Detail)
	CodeInternal      = "INTERNAL"
)

// messages are what whoever publishes reads, in plain Spanish.
var messages = map[string]string{
	CodeNotConfigured: "GitHub no está configurado en Odicea. Pide a TI que lo configure.",
	CodeStateInvalid:  "El enlace para conectar GitHub venció o no es válido. Vuelve a intentarlo desde Odicea.",
	CodeNotYours:      "Tu cuenta de GitHub no administra esa instalación: debe instalarla quien es dueño de la cuenta u organización en GitHub.",
	CodeTaken:         "Esa cuenta de GitHub ya está conectada a otra organización de Odicea.",
	CodeNoAccess:      "Odicea ya no tiene acceso a este repositorio en GitHub. Pide a TI que revise la instalación.",
	CodeBranchMissing: "La rama conectada ya no existe en GitHub.",
	CodeRootMissing:   "La carpeta elegida no existe en ese commit.",
	CodeTooLarge:      "El repositorio es demasiado grande para publicarlo (máximo 100 MB comprimido, 500 MB y 20.000 archivos).",
	CodeImportFailed:  "No pudimos descargar el código de GitHub. Vuelve a intentarlo.",
	CodeProviderDown:  "GitHub no respondió. Lo intentaremos de nuevo; si sigue, vuelve a publicar más tarde.",
	CodeDeployRefused: "No se pudo empezar la publicación. Revisa el detalle.",
	CodeInternal:      "Algo falló de nuestro lado al traer tu código. Vuelve a intentarlo.",
}

// Message is the plain-Spanish text of a code (the internal one for unknown codes).
func Message(code string) string {
	if m, ok := messages[code]; ok {
		return m
	}
	return messages[CodeInternal]
}

// Known reports whether code is one of this package's failure codes.
func Known(code string) bool {
	_, ok := messages[code]
	return ok
}

// maxDetail bounds the provider's words kept with a failure.
const maxDetail = 2000

// FailureOf is a failure with code's message and detail cut to fit.
func FailureOf(code, detail string) Failure {
	if _, ok := messages[code]; !ok {
		code = CodeInternal
	}
	if len(detail) > maxDetail {
		detail = detail[:maxDetail]
	}
	return Failure{Code: code, Message: Message(code), Detail: detail}
}
