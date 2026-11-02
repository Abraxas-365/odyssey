package sourcegithub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ManifestOptions describe the GitHub App to register through the manifest
// flow (https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).
type ManifestOptions struct {
	Name string
	// PublicURL is the API's base URL as GitHub and the browser reach it,
	// e.g. http://localhost:8095. The setup/callback URL hangs off it.
	PublicURL string
	// WebhookURL defaults to PublicURL + "/webhooks/github".
	WebhookURL  string
	Description string
	// Public makes the App installable by accounts other than its owner.
	Public bool
}

// manifestDoc is the JSON GitHub's manifest parameters expect.
type manifestDoc struct {
	Name                  string            `json:"name,omitempty"`
	URL                   string            `json:"url"`
	Description           string            `json:"description,omitempty"`
	HookAttributes        manifestHook      `json:"hook_attributes"`
	RedirectURL           string            `json:"redirect_url"`
	CallbackURLs          []string          `json:"callback_urls"`
	SetupURL              string            `json:"setup_url"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	SetupOnUpdate         bool              `json:"setup_on_update"`
	Public                bool              `json:"public"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
}

type manifestHook struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// Manifest renders the manifest JSON for o. redirectURL is where GitHub
// sends the browser (with the temporary code) once the App is created.
//
// Only "push" is subscribed: installation and installation_repositories
// webhooks are always delivered to a GitHub App.
func Manifest(o ManifestOptions, redirectURL string) ([]byte, error) {
	base := strings.TrimRight(strings.TrimSpace(o.PublicURL), "/")
	if base == "" {
		return nil, errors.New("manifest: public URL is required")
	}
	if redirectURL == "" {
		return nil, errors.New("manifest: redirect URL is required")
	}
	hook := strings.TrimSpace(o.WebhookURL)
	if hook == "" {
		hook = base + "/webhooks/github"
	}
	setup := base + "/github/setup"
	return json.Marshal(manifestDoc{
		Name:                  o.Name,
		URL:                   base,
		Description:           o.Description,
		HookAttributes:        manifestHook{URL: hook, Active: true},
		RedirectURL:           redirectURL,
		CallbackURLs:          []string{setup},
		SetupURL:              setup,
		RequestOAuthOnInstall: true,
		SetupOnUpdate:         true,
		Public:                o.Public,
		DefaultPermissions: map[string]string{
			"contents": "read",
			// members:read lets the setup callback ask, with the user's own
			// token, whether the user administers the installed organization.
			"members":  "read",
			"metadata": "read",
			"statuses": "write",
		},
		DefaultEvents: []string{"push"},
	})
}

// ManifestPostURL is where the browser POSTs the manifest: the personal
// account's page, or the organization's when org is set.
func ManifestPostURL(webURL, org string) string {
	webURL = strings.TrimRight(webURL, "/")
	if org = strings.TrimSpace(org); org != "" {
		return webURL + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return webURL + "/settings/apps/new"
}

// AppCredentials is what GitHub returns once a manifest is converted. Its
// fields are secrets (except ID, Slug, ClientID and HTMLURL): never log it.
type AppCredentials struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	HTMLURL       string `json:"html_url"`
}

// String redacts the secrets so a stray %v cannot leak them.
func (c AppCredentials) String() string {
	return fmt.Sprintf("AppCredentials{ID:%d Slug:%s ClientID:%s}", c.ID, c.Slug, c.ClientID)
}

// GoString keeps %#v from printing the secrets.
func (c AppCredentials) GoString() string { return c.String() }

// ConvertManifest exchanges the temporary code of the manifest flow for the
// App's credentials (POST /app-manifests/{code}/conversions, unauthenticated).
func ConvertManifest(ctx context.Context, client *http.Client, apiURL, code string) (AppCredentials, error) {
	var out AppCredentials
	if strings.TrimSpace(code) == "" {
		return out, errors.New("manifest conversion: empty code")
	}
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(apiURL, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return out, fmt.Errorf("manifest conversion: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("manifest conversion: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, fmt.Errorf("manifest conversion: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Message == "" {
			e.Message = "no message"
		}
		return out, fmt.Errorf("manifest conversion: GitHub answered %d: %s", resp.StatusCode, e.Message)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return AppCredentials{}, fmt.Errorf("manifest conversion: decoding response: %w", err)
	}
	if out.ID == 0 || out.PEM == "" || out.WebhookSecret == "" {
		return AppCredentials{}, errors.New("manifest conversion: GitHub's answer lacks the App id, private key or webhook secret")
	}
	return out, nil
}
