package sourcefake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// Sign is GitHub's X-Hub-Signature-256 value for body: "sha256=<hex>".
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Sign signs body with the fake's webhook secret.
func (f *Fake) Sign(body []byte) string { return Sign(f.opts.WebhookSecret, body) }

func (f *Fake) VerifyWebhook(signature string, body []byte) error {
	want := Sign(f.opts.WebhookSecret, body)
	if !hmac.Equal([]byte(strings.TrimSpace(signature)), []byte(want)) {
		return errx.Unauthorized("invalid webhook signature")
	}
	return nil
}

// The minimal GitHub payload shapes (copied, not imported: the fake must not
// depend on go-github).
type (
	installationRef struct {
		ID int64 `json:"id"`
	}
	author struct {
		Name     string `json:"name,omitempty"`
		Username string `json:"username,omitempty"`
	}
	headCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  author `json:"author"`
	}
	repoRef struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name,omitempty"`
	}
	pushPayload struct {
		Ref          string           `json:"ref"`
		After        string           `json:"after"`
		Deleted      bool             `json:"deleted"`
		Repository   *repoRef         `json:"repository"`
		Installation *installationRef `json:"installation"`
		HeadCommit   *headCommit      `json:"head_commit"`
	}
	installationPayload struct {
		Action              string           `json:"action"`
		Installation        *installationRef `json:"installation"`
		RepositoriesRemoved []repoRef        `json:"repositories_removed"`
	}
)

// PushPayload is a push webhook body for c landing on branch.
func PushPayload(inst, repoID int64, fullName, branch string, c source.Commit) []byte {
	body, _ := json.Marshal(pushPayload{
		Ref:          "refs/heads/" + branch,
		After:        c.SHA,
		Repository:   &repoRef{ID: repoID, FullName: fullName},
		Installation: &installationRef{ID: inst},
		HeadCommit:   &headCommit{ID: c.SHA, Message: c.Title, Author: author{Name: c.Author, Username: c.Author}},
	})
	return body
}

// InstallationPayload is an installation (or, with removed ids,
// installation_repositories) webhook body.
func InstallationPayload(action string, inst int64, removed ...int64) []byte {
	p := installationPayload{Action: action, Installation: &installationRef{ID: inst}}
	for _, id := range removed {
		p.RepositoriesRemoved = append(p.RepositoriesRemoved, repoRef{ID: id})
	}
	body, _ := json.Marshal(p)
	return body
}

func (f *Fake) PushEvent(payload []byte) (source.PushEvent, error) {
	f.mu.Lock()
	err := f.failures["PushEvent"]
	f.mu.Unlock()
	if err != nil {
		return source.PushEvent{}, err
	}
	var p pushPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return source.PushEvent{}, errx.Wrap(err, "the push event is not valid JSON", errx.TypeValidation)
	}
	if p.Installation == nil || p.Repository == nil {
		return source.PushEvent{}, errx.Validation("the push event has no installation or repository")
	}
	out := source.PushEvent{
		Installation: p.Installation.ID,
		RepositoryID: p.Repository.ID,
		Repository:   p.Repository.FullName,
		Deleted:      p.Deleted,
	}
	if branch, ok := strings.CutPrefix(p.Ref, "refs/heads/"); ok {
		out.Branch = branch
	}
	if hc := p.HeadCommit; hc != nil && hc.ID != "" {
		who := hc.Author.Username
		if who == "" {
			who = hc.Author.Name
		}
		title, _, _ := strings.Cut(hc.Message, "\n")
		out.Head = source.Commit{SHA: hc.ID, Title: strings.TrimSpace(title), Author: who}
	} else {
		out.Head = source.Commit{SHA: p.After}
	}
	return out, nil
}

func (f *Fake) InstallationEvent(event string, payload []byte) (source.InstallationEvent, error) {
	f.mu.Lock()
	err := f.failures["InstallationEvent"]
	f.mu.Unlock()
	if err != nil {
		return source.InstallationEvent{}, err
	}
	if event != "installation" && event != "installation_repositories" {
		return source.InstallationEvent{}, errx.Validation("not an installation event: " + event)
	}
	var p installationPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return source.InstallationEvent{}, errx.Wrap(err, "the installation event is not valid JSON", errx.TypeValidation)
	}
	if p.Installation == nil {
		return source.InstallationEvent{}, errx.Validation("the installation event has no installation")
	}
	out := source.InstallationEvent{Installation: p.Installation.ID, Action: p.Action}
	if event == "installation_repositories" {
		for _, r := range p.RepositoriesRemoved {
			out.Removed = append(out.Removed, r.ID)
		}
	}
	return out, nil
}
