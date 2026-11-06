// Package accountiam reads and ends the caller's assistant connections in
// IAMKit (GET/DELETE /identity/v1/me/connected-apps), with the caller's own
// token: IAMKit answers about a user's connections only to that user.
package accountiam

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/apierror"
	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/Abraxas-365/odyssey/internal/account"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// API is the part of authclient.Client used here.
type API interface {
	ListConnectedApps(ctx context.Context, token, environment, audience, organization string) ([]authclient.ConnectedApp, error)
	DisconnectApp(ctx context.Context, token, environment, audience, client, organization string) error
}

// Connections implements account.Connections. Environment and Audience are
// the console token's (IAMKIT_ENVIRONMENT_ID, IAMKIT_AUDIENCE); Resource is
// the MCP server's URI (the same audience: Odicea's REST API and /mcp share
// it), so only assistants connected to Odicea are listed. FirstParty are
// Odicea's own OAuth clients (the console's, IAMKIT_WEB_CLIENT_ID): they hold
// sessions at the same resource but are not assistants.
type Connections struct {
	API         API
	Environment string
	Audience    string
	Resource    string
	FirstParty  []string
}

var _ account.Connections = Connections{}

// timeout bounds each IAMKit call: /me must answer even when IAMKit is slow.
const timeout = 3 * time.Second

func (c Connections) List(ctx context.Context, organization identity.OrganizationID, token string) ([]account.Connected, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	apps, err := c.API.ListConnectedApps(ctx, token, c.Environment, c.Audience, organization.String())
	if err != nil {
		return nil, mapError(err, "list connected assistants")
	}
	out := []account.Connected{}
	for _, a := range apps {
		// Odicea's own console is not an assistant. Anything else at the MCP
		// server is, consented or not: under IAMKit's admin-granted consent
		// an assistant holds sessions without the user ever being asked.
		if a.Resource != c.Resource || slices.Contains(c.FirstParty, a.Client.ClientID) {
			continue
		}
		out = append(out, account.Connected{ID: a.Client.ID, ClientID: a.Client.ClientID, ConsentedAt: a.ConsentedAt, LastUsedAt: a.LastUsedAt})
	}
	return out, nil
}

func (c Connections) Disconnect(ctx context.Context, organization identity.OrganizationID, token, id string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return mapError(c.API.DisconnectApp(ctx, token, c.Environment, c.Audience, id, organization.String()), "disconnect assistant")
}

// mapError keeps IAMKit's verdict on the user's request (404, 403, 401) and
// reports the rest as IAMKit failing.
func mapError(err error, op string) error {
	if err == nil {
		return nil
	}
	var api *apierror.Error
	if errors.As(err, &api) {
		switch api.HTTPStatus {
		case 404:
			return errx.NotFound("assistant connection not found")
		case 403:
			return errx.Forbidden("IAMKit does not let this session manage its assistants")
		case 401:
			return errx.Unauthorized("the session expired: sign in again")
		}
	}
	return errx.Wrap(err, op+": IAMKit did not answer", errx.TypeExternal)
}
