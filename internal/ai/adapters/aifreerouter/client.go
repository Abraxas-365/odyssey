// Package aifreerouter is the ai.Provider on a freerouter gateway: each key
// is a service account that freerouter's IAMKit holds, made and revoked
// through its /api/v1/service-accounts. The caller authenticates with its own
// service key (Authorization: Bearer ik_svc_…) holding
// freerouter:service-accounts:read and :write, and every permission it grants
// (freerouter:gateway:invoke).
package aifreerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Options configure the client.
type Options struct {
	// URL is freerouter's API address (https://host, no trailing /).
	URL string
	// Key is the platform's service key.
	Key string
	// HTTP is the client to use (default: 15 s timeout).
	HTTP *http.Client
}

type Client struct {
	url  string
	key  string
	http *http.Client
}

func New(o Options) *Client {
	c := o.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{url: strings.TrimRight(o.URL, "/"), key: o.Key, http: c}
}

var _ ai.Provider = (*Client)(nil)

// The key is for the gateway only; gateway:invoke is the one permission an
// app's key carries.
const permission = "freerouter:gateway:invoke"

type created struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// failure is the gateway's error body. Freerouter answers two shapes: its
// handlers a flat {"code","message"} and its middleware (authentication) a
// nested {"error":{"code","message"}}; both are read.
type failure struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Error   *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// detail is the gateway's own words, from whichever shape it used.
func (f failure) detail() string {
	switch {
	case f.Error != nil && f.Error.Message != "":
		return f.Error.Message
	case f.Error != nil && f.Error.Code != "":
		return f.Error.Code
	case f.Message != "":
		return f.Message
	}
	return f.Code
}

// CreateKey makes a service account called name that expires never: its
// lifecycle is the platform's (rotation, disabling).
func (c *Client) CreateKey(ctx context.Context, name string) (ai.ProviderKey, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "permissions": []string{permission}, "expires_in": "never"})
	var out created
	if err := c.do(ctx, http.MethodPost, "/api/v1/service-accounts", body, &out, http.StatusCreated, http.StatusOK); err != nil {
		return ai.ProviderKey{}, err
	}
	if out.ID == "" || out.Secret == "" {
		return ai.ProviderKey{}, errx.External("The AI gateway answered without a key.")
	}
	return ai.ProviderKey{ID: out.ID, Secret: out.Secret}, nil
}

// RevokeKey revokes a service account; one that is already gone is a success.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	err := c.do(ctx, http.MethodDelete, "/api/v1/service-accounts/"+url.PathEscape(id), nil, nil, http.StatusNoContent, http.StatusOK, http.StatusNotFound)
	return err
}

// ListKeys lists the live service accounts whose name starts with prefix
// (freerouter has no filter: it is applied here).
func (c *Client) ListKeys(ctx context.Context, prefix string) ([]ai.KeyInfo, error) {
	var all []account
	if err := c.do(ctx, http.MethodGet, "/api/v1/service-accounts", nil, &all, http.StatusOK); err != nil {
		return nil, err
	}
	out := []ai.KeyInfo{}
	for _, a := range all {
		if strings.HasPrefix(a.Name, prefix) {
			out = append(out, ai.KeyInfo{ID: a.ID, Name: a.Name})
		}
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, into any, ok ...int) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, rd)
	if err != nil {
		return errx.Internal("the AI gateway request could not be built")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		// err can carry the URL, never the key
		return errx.External("The AI gateway is not reachable.")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	for _, s := range ok {
		if res.StatusCode == s {
			if into != nil && len(raw) > 0 {
				if err := json.Unmarshal(raw, into); err != nil {
					return errx.External("The AI gateway answered something unexpected.")
				}
			}
			return nil
		}
	}
	var f failure
	_ = json.Unmarshal(raw, &f)
	return refusal(res.StatusCode, f)
}

// refusal turns the gateway's error into ours. 401/403 mean the platform's own
// key is wrong (IT's to fix), 5xx and the rest may pass: all retriable.
func refusal(status int, f failure) error {
	detail := f.detail()
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errx.External(fmt.Sprintf("The AI gateway refused the platform's key (%d). Ask IT to check ODYSSEY_FREEROUTER_KEY.", status))
	case status == http.StatusTooManyRequests || status >= 500:
		return errx.External(fmt.Sprintf("The AI gateway is not available now (%d).", status))
	}
	return errx.External(fmt.Sprintf("The AI gateway refused the request (%d): %s", status, detail))
}
