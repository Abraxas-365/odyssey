package sourcegithub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/source"
)

// VerifyWebhook checks X-Hub-Signature-256 ("sha256=<hex>") over body.
func (p *Provider) VerifyWebhook(signature string, body []byte) error {
	invalid := errx.Unauthorized("invalid webhook signature")
	hexSum, ok := strings.CutPrefix(strings.TrimSpace(signature), "sha256=")
	if !ok {
		return invalid
	}
	got, err := hex.DecodeString(hexSum)
	if err != nil || len(got) != sha256.Size {
		return invalid
	}
	mac := hmac.New(sha256.New, p.webhookSecret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return invalid
	}
	return nil
}

const headsPrefix = "refs/heads/"

// PushEvent decodes a push delivery.
func (p *Provider) PushEvent(payload []byte) (source.PushEvent, error) {
	var ev github.PushEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return source.PushEvent{}, errx.Wrap(err, "the push event is not valid JSON", errx.TypeValidation)
	}
	if ev.Installation == nil || ev.Repo == nil || ev.Repo.ID == nil {
		return source.PushEvent{}, errx.Validation("the push event has no installation or repository")
	}
	out := source.PushEvent{
		Installation: ev.Installation.GetID(),
		RepositoryID: ev.Repo.GetID(),
		Repository:   ev.Repo.GetFullName(),
		Deleted:      ev.GetDeleted(),
	}
	if branch, ok := strings.CutPrefix(ev.GetRef(), headsPrefix); ok {
		out.Branch = branch
	}
	if hc := ev.HeadCommit; hc != nil && hc.GetID() != "" {
		author := hc.GetAuthor().GetLogin()
		if author == "" {
			author = hc.GetAuthor().GetName()
		}
		out.Head = source.Commit{SHA: hc.GetID(), Title: firstLine(hc.GetMessage()), Author: author}
	} else {
		out.Head = source.Commit{SHA: ev.GetAfter()}
	}
	return out, nil
}

// InstallationEvent decodes an "installation" or "installation_repositories"
// delivery.
func (p *Provider) InstallationEvent(event string, payload []byte) (source.InstallationEvent, error) {
	switch event {
	case "installation":
		var ev github.InstallationEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return source.InstallationEvent{}, errx.Wrap(err, "the installation event is not valid JSON", errx.TypeValidation)
		}
		if ev.Installation == nil || ev.Installation.ID == nil {
			return source.InstallationEvent{}, errx.Validation("the installation event has no installation")
		}
		return source.InstallationEvent{Installation: ev.Installation.GetID(), Action: ev.GetAction()}, nil
	case "installation_repositories":
		var ev github.InstallationRepositoriesEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return source.InstallationEvent{}, errx.Wrap(err, "the installation event is not valid JSON", errx.TypeValidation)
		}
		if ev.Installation == nil || ev.Installation.ID == nil {
			return source.InstallationEvent{}, errx.Validation("the installation event has no installation")
		}
		out := source.InstallationEvent{Installation: ev.Installation.GetID(), Action: ev.GetAction()}
		for _, r := range ev.RepositoriesRemoved {
			out.Removed = append(out.Removed, r.GetID())
		}
		return out, nil
	}
	return source.InstallationEvent{}, errx.Validation("not an installation event: " + event)
}
