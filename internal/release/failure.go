package release

import (
	"errors"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Failed builds the error a deploy step returns to fail the deployment for
// good (no retries): a known code and a message for the person deploying.
func Failed(code, message string) error {
	return errx.Business(message).WithCode(code)
}

// FailureOf explains why a deployment failed at step, from the error that
// ended its workflow: a coded error from the steps keeps its code; anything
// else (retries exhausted, a timeout) is named after the step.
//
// When OnFailure itself had to be retried, the engine hands it the cause as
// text ("[CODE] message"); a step's coded failure is recognised there too.
func FailureOf(step string, cause error) Failure {
	var e *errx.Error
	if errors.As(cause, &e) && stepCodes[e.Code] {
		return Failure{Code: e.Code, Message: e.Message}
	}
	if cause != nil {
		if code, msg, ok := strings.Cut(strings.TrimPrefix(cause.Error(), "["), "] "); ok && strings.HasPrefix(cause.Error(), "[") && stepCodes[code] {
			return Failure{Code: code, Message: msg}
		}
	}
	switch step {
	case StepApply:
		return Failure{Code: CodeApplyFailed, Message: "No se pudo entregar la app al clúster."}
	case StepWaitReady:
		return Failure{Code: CodeNotReady, Message: "La app no quedó lista a tiempo."}
	}
	return Failure{Code: CodePromoteFailed, Message: "No se pudo dejar la nueva versión en línea."}
}

// stepCodes are the failures a step reports itself (the rest are inferred).
var stepCodes = map[string]bool{CodeUnhealthy: true, CodeNotReady: true, CodeApplyFailed: true, CodeImagePull: true, CodeCrashed: true}

// Unready explains, for the person deploying, why the cluster reports the
// app failed: plain consequence first, then the cluster's own detail.
func Unready(s AppStatus) error {
	code, msg := CodeUnhealthy, "La app arrancó pero no responde en su ruta de salud."
	switch s.Reason {
	case FailureImagePull:
		code, msg = CodeImagePull, "El clúster no pudo descargar la imagen. Revisa que exista y que el clúster tenga acceso al registro."
	case FailureCrash:
		code, msg = CodeCrashed, "La app se cierra al arrancar."
	case FailureConfig:
		code, msg = CodeApplyFailed, "El clúster no pudo aplicar la configuración de la app."
	}
	if s.Message != "" {
		msg += " " + s.Message
	}
	return Failed(code, msg)
}
