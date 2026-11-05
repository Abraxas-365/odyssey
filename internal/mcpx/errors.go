package mcpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/gate"
)

// nextStepKey is the errx detail a tool sets with NextStep.
const nextStepKey = "next_step"

// NextStep tells the agent what to do about err: an action it can take
// ("Usa list_apps para ver los slugs") or a link for the user ("Conecta
// GitHub en la consola: <url>"). Spanish, one sentence.
func NextStep(err *errx.Error, step string) *errx.Error {
	return err.WithDetail(nextStepKey, step)
}

// toolError is what an agent reads when a call fails: the text content of
// an isError result (MCP: tool errors are results, not protocol errors, so
// the model can see and fix them).
type toolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	NextStep  string `json:"next_step,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (e toolError) Error() string {
	b, _ := json.Marshal(map[string]toolError{"error": e})
	return string(b)
}

// defaultSteps apply when the tool set no next_step.
var defaultSteps = map[errx.Type]string{
	errx.TypeAuthorization: "Tu cuenta o esta conexión del asistente no tiene ese permiso: pídeselo a TI o vuelve a conectar el asistente.",
	errx.TypeNotFound:      "Revisa el nombre: list_apps muestra las apps de tu organización.",
	errx.TypeInternal:      "Reintenta en unos segundos; si sigue fallando, avisa a TI con el request_id.",
	errx.TypeExternal:      "Un servicio del que depende Odicea no respondió: reintenta en unos segundos.",
}

// codeSteps are the next_step of the refusals every governed tool can answer
// (IT's rules, internal/gate): worded once here so no tool forgets them, and
// before defaultSteps (POLICY_DENIED is a 403, which would otherwise read as
// a missing permission). A tool's own NextStep still wins.
var codeSteps = map[string]string{
	gate.CodeDenied: "Una regla de TI no permite esto: muéstrale el mensaje al usuario tal cual y no reintentes ni busques otra vía; " +
		"si cree que es un error, que lo hable con TI.",
	gate.CodeApprovalRequired: "Esto necesita aprobación de TI: muéstrale el mensaje al usuario tal cual y no reintentes; " +
		"que TI lo autorice o cambie la regla (todavía no se puede pedir la aprobación desde el asistente).",
}

// toolErrorOf maps err (an *errx.Error from the domain, or anything else:
// an internal failure, whose cause is logged and never shown) to the
// result error of a call.
func toolErrorOf(ctx context.Context, err error, log *slog.Logger) error {
	requestID := event.MetaFrom(ctx).CorrelationID
	var e *errx.Error
	if !errors.As(err, &e) {
		e = errx.Internal(errx.GenericMessage)
	}
	if e.HTTPStatus >= 500 {
		log.ErrorContext(ctx, "mcp tool failed", "request_id", requestID, "code", e.Code, "err", err)
	}
	out := toolError{Code: e.Code, Message: e.Message, RequestID: requestID}
	if e.HTTPStatus >= 500 && e.Type == errx.TypeInternal {
		out.Message = errx.GenericMessage
	}
	if f, ok := e.Details["field"].(string); ok {
		out.Field = f
	}
	if s, ok := e.Details[nextStepKey].(string); ok && s != "" {
		out.NextStep = s
	} else if s, ok := codeSteps[e.Code]; ok {
		out.NextStep = s
	} else {
		out.NextStep = defaultSteps[e.Type]
	}
	return out
}
