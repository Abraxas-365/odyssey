package mcpx

import (
	"context"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Codes of a confirmation that did not happen.
const (
	CodeConfirmationRequired = "CONFIRMATION_REQUIRED" // 409: ask the user, then repeat with confirm: true
	CodeConfirmationDeclined = "CONFIRMATION_DECLINED" // 409: the user said no
)

type sessionKey struct{}

// withSession keeps the call's MCP session so a tool can ask the user
// (Confirm). Typed does it for every call.
func withSession(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Session == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, req.Session)
}

// Confirm is how a tool that does something hard to undo (going back to an
// earlier version of a production app, opening an app to anyone with the link)
// gets the user's yes before it acts. In order:
//
//  1. confirmed (the input's `confirm: true`): the assistant says the user
//     agreed; go on.
//  2. the client supports elicitation: ask the user in the client; accept goes
//     on, decline or cancel is 409 CONFIRMATION_DECLINED.
//  3. otherwise: 409 CONFIRMATION_REQUIRED, whose next_step tells the assistant
//     to ask the user and repeat the call with confirm: true.
//
// Odicea's MCP endpoint is stateless, so today most clients land on 3; a
// client on a stateful transport gets 2 without any change in the tools.
// message is the question, in plain Spanish.
func Confirm(ctx context.Context, confirmed bool, message string) error {
	if confirmed {
		return nil
	}
	if ss, ok := ctx.Value(sessionKey{}).(*mcp.ServerSession); ok {
		res, err := ss.Elicit(ctx, &mcp.ElicitParams{Message: message, RequestedSchema: map[string]any{
			"type": "object", "properties": map[string]any{},
		}})
		if err == nil {
			if res.Action == "accept" {
				return nil
			}
			return NextStep(errx.Conflict("El usuario no lo confirmó.").WithCode(CodeConfirmationDeclined),
				"No lo hagas y dile al usuario que no se cambió nada.")
		}
		// the client cannot be asked (not supported, or a stateless
		// transport): fall back to the assistant asking
	}
	return NextStep(errx.Conflict(message).WithCode(CodeConfirmationRequired),
		"Pregúntale al usuario, con estas palabras, si quiere seguir. Si dice que sí, repite la llamada con confirm: true; si no, no hagas nada.")
}
