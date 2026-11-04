package mcpx

import (
	"context"
	"log/slog"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// adder registers a tool's typed handler on a per-request SDK server.
type adder func(s *mcp.Server, t *mcp.Tool, log *slog.Logger)

// Handler is a tool's code: it runs only for a principal holding the tool's
// Permissions (checked again on every call) and returns the tool's output,
// or an error (errx, mapped by the transport to an isError result with a
// next_step for the agent; see NextStep).
type Handler[In, Out any] func(ctx context.Context, caller Principal, in In) (Out, error)

// Typed gives t its handler. In and Out are structs: their JSON Schemas
// (from the json and jsonschema tags) are the tool's inputSchema and
// outputSchema, and the SDK validates arguments against In before h runs.
func Typed[In, Out any](t Tool, h Handler[In, Out]) Tool {
	permissions := t.Permissions
	t.add = func(s *mcp.Server, tool *mcp.Tool, log *slog.Logger) {
		mcp.AddTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			var zero Out
			ctx = callContext(ctx, req)
			caller, ok := FromContext(ctx)
			if !ok {
				return nil, zero, toolErrorOf(ctx, errx.Unauthorized("missing or invalid bearer token"), log)
			}
			// The server only lists what the caller may use; a call is
			// checked again so a stale tools/list never widens access.
			if !caller.Can(permissions...) {
				return nil, zero, toolErrorOf(ctx, errx.Forbidden("insufficient permissions"), log)
			}
			out, err := h(ctx, caller, in)
			if err != nil {
				return nil, zero, toolErrorOf(ctx, err, log)
			}
			return nil, out, nil
		})
	}
	return t
}
