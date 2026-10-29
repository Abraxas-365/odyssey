package build

import (
	"errors"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Error codes of failed builds and refused requests.
const (
	CodeUnsupported   = "UNSUPPORTED_APP" // the builder does not know how to build or start it
	CodeFailed        = "BUILD_FAILED"    // a build step failed
	CodeTimeout       = "BUILD_TIMEOUT"   // it took longer than Timeout
	CodeSourceInvalid = "SOURCE_INVALID"  // the archive is not a usable source
	CodePushFailed    = "PUSH_FAILED"     // the registry refused the image
	CodeNoBuilder     = "NO_BUILDER"      // the environment's cluster cannot build
	CodeNoCluster     = "NO_CLUSTER"      // no cluster to build in
	CodeSourceExpired = "SOURCE_EXPIRED"  // the source was swept before it was built
	CodeInternal      = "INTERNAL"
)

// CodeSourceQuota refuses an upload (not a build): the organization's
// sources fill its quota and none can be dropped early.
const CodeSourceQuota = "SOURCE_QUOTA"

// maxDetail bounds the runner's words kept with a failure.
const maxDetail = 4000

// messages are what whoever publishes reads, in plain Spanish.
var messages = map[string]string{
	CodeUnsupported:   "No reconocimos qué tipo de app es ni cómo arrancarla. Revisa que la carpeta tenga el código completo (por ejemplo package.json con un script start, requirements.txt o go.mod).",
	CodeFailed:        "Falló al construir tu app. Los detalles técnicos muestran el paso que falló.",
	CodeTimeout:       "Construir tu app tardó demasiado y se detuvo.",
	CodeSourceInvalid: "No pudimos leer la carpeta subida. Vuelve a elegirla y publica otra vez.",
	CodePushFailed:    "Se construyó la app pero no se pudo guardar la imagen. Pide ayuda a TI.",
	CodeNoBuilder:     "El clúster de esta app no está preparado para construir. Pide a TI que configure un registry.",
	CodeNoCluster:     "No hay un clúster donde construir esta app. Pide a TI que registre uno.",
	CodeSourceExpired: "La carpeta subida ya no está disponible. Vuelve a publicarla.",
	CodeInternal:      "Algo falló de nuestro lado al construir tu app. Vuelve a intentarlo.",
}

// Message is the plain-Spanish text of a code (the internal one for unknown codes).
func Message(code string) string {
	if m, ok := messages[code]; ok {
		return m
	}
	return messages[CodeInternal]
}

// FailureOf is the failure of a build the runner reported as failed.
func FailureOf(o Observation) Failure {
	code := CodeFailed
	switch o.Reason {
	case ReasonUnsupported:
		code = CodeUnsupported
	case ReasonSource:
		code = CodeSourceInvalid
	case ReasonPush:
		code = CodePushFailed
	case ReasonTimeout:
		code = CodeTimeout
	}
	return Failure{Code: code, Message: Message(code), Detail: detail(o.Message)}
}

// FailureFromError is the failure of a build that ended by an error at step
// (a coded refusal, retries exhausted, the timeout).
func FailureFromError(step string, err error) Failure {
	if step == StepWait && err != nil && strings.Contains(err.Error(), "timed out") {
		return Failure{Code: CodeTimeout, Message: Message(CodeTimeout)}
	}
	var e *errx.Error
	if errors.As(err, &e) && e.Code != "" && e.Code != CodeInternal {
		if _, known := messages[e.Code]; known {
			return Failure{Code: e.Code, Message: Message(e.Code), Detail: detail(e.Message)}
		}
	}
	d := ""
	if err != nil {
		d = err.Error()
	}
	return Failure{Code: CodeInternal, Message: Message(CodeInternal), Detail: detail(d)}
}

// Refused is the coded error of a build that cannot start (a runner's answer
// to Dispatch): the build fails with code instead of being retried.
func Refused(code, detail string) error {
	return errx.Business(detail).WithCode(code)
}

func detail(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if len(s) > maxDetail {
		s = strings.ToValidUTF8("…"+s[len(s)-maxDetail:], "")
	}
	return s
}
