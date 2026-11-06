package release

import (
	"errors"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/project"
)

// BuildFailed is the error the build step fails a deployment with: its
// build's code, message and the builder's words.
func BuildFailed(f build.Failure) error {
	code := f.Code
	if code == build.CodeInternal || code == "" {
		code = CodeBuildInternal
	}
	msg := f.Message
	if msg == "" {
		msg = build.Message(f.Code)
	}
	return Failed(code, strings.ReplaceAll(msg, ": ", " — "), f.Detail)
}

// Failed builds the error a deploy step returns to fail the deployment for
// good (no retries): a known code, a plain-Spanish message for the person
// deploying and, optionally, the cluster's own technical detail (kept apart:
// the console shows it only with technical details on).
//
// Messages never contain ": " — that separates the detail when the cause
// travels as text.
func Failed(code, message string, detail ...string) error {
	if d := strings.Join(detail, " "); d != "" {
		return errx.Wrap(errors.New(d), message, errx.TypeBusiness).WithCode(code)
	}
	return errx.Business(message).WithCode(code)
}

// FailureOf explains why a deployment failed at step, from the error that
// ended its workflow: a coded error from the steps keeps its code; anything
// else (retries exhausted, a timeout) is named after the step.
//
// When OnFailure itself had to be retried, the engine hands it the cause as
// text ("[CODE] message: detail"); a step's coded failure is recognised there
// too.
func FailureOf(step string, cause error) Failure {
	var e *errx.Error
	if errors.As(cause, &e) && stepCodes[e.Code] {
		f := Failure{Code: e.Code, Message: e.Message}
		if e.Err != nil {
			f.Detail = e.Err.Error()
		}
		return f
	}
	if cause != nil {
		if code, rest, ok := strings.Cut(strings.TrimPrefix(cause.Error(), "["), "] "); ok && strings.HasPrefix(cause.Error(), "[") && stepCodes[code] {
			msg, detail, _ := strings.Cut(rest, ": ")
			return Failure{Code: code, Message: msg, Detail: detail}
		}
	}
	switch step {
	case StepBuild:
		return Failure{Code: CodeBuildInternal, Message: build.Message(build.CodeInternal)}
	case StepApply:
		return Failure{Code: CodeApplyFailed, Message: "No se pudo entregar la app al clúster."}
	case StepWaitReady:
		return Failure{Code: CodeNotReady, Message: "La app no quedó lista a tiempo."}
	}
	return Failure{Code: CodePromoteFailed, Message: "No se pudo dejar la nueva versión en línea."}
}

// stepCodes are the failures a step reports itself (the rest are inferred);
// the build step reports its build's own code (build.Code*).
var stepCodes = map[string]bool{CodeUnhealthy: true, CodeNotReady: true, CodeApplyFailed: true, CodeImagePull: true, CodeCrashed: true,
	CodeResourceNotReady: true, build.CodeUnsupported: true, build.CodeFailed: true, build.CodeTimeout: true, build.CodeSourceInvalid: true,
	build.CodePushFailed: true, build.CodeNoBuilder: true, build.CodeNoCluster: true, build.CodeSourceExpired: true,
	CodeBuildInternal: true, CodePolicyDenied: true, project.CodeServicesUnavailable: true}

// Unready explains, for the person deploying, why the cluster reports the
// app failed: the plain consequence, with the cluster's words as the detail.
func Unready(s AppStatus) error {
	code, msg := CodeUnhealthy, "La app arrancó pero no responde en su ruta de salud."
	switch s.Reason {
	case FailureImagePull:
		code, msg = CodeImagePull, "El clúster no pudo descargar la imagen. Revisa que exista y que el clúster tenga acceso al registro."
	case FailureCrash:
		code, msg = CodeCrashed, "La app se cierra al arrancar."
	case FailureConfig:
		code, msg = CodeApplyFailed, "El clúster no pudo aplicar la configuración de la app."
	case FailureResource:
		code, msg = CodeResourceNotReady, "Un recurso de la app (base de datos, caché o archivos) no estuvo listo a tiempo."
	}
	return Failed(code, msg, s.Message)
}
