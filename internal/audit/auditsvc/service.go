// Package auditsvc implements audit.Commands and audit.Queries.
package auditsvc

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/Abraxas-365/odyssey/internal/assistant"
	"github.com/Abraxas-365/odyssey/internal/audit"
	"github.com/Abraxas-365/odyssey/internal/channel"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// Options tune the service; zero values are the defaults.
type Options struct {
	// Assistants are the OAuth clients IAMKit made for the template
	// assistants (config MCP.TemplateClients): they name the channels.
	Assistants assistant.Registered
	// Directory names the people; nil leaves the names empty.
	Directory audit.Directory
	// Now is the clock (tests set it).
	Now func() time.Time
}

// deploymentStarted is the type a publication is counted by (release's
// event; audit reads it as data, never imports release).
const deploymentStarted = "deployment.started"

// peopleLimit bounds the people filter: the most recently active.
const peopleLimit = 200

type Service struct {
	repo audit.Repository
	tx   txn.Runner
	opts Options
}

func New(repo audit.Repository, tx txn.Runner, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{repo: repo, tx: tx, opts: opts}
}

var (
	_ audit.Commands = (*Service)(nil)
	_ audit.Queries  = (*Service)(nil)
)

// Record appends e to its organization's chain. The head row lock
// serializes writers per organization; a re-delivered event is a no-op.
// The trail is the end of the line: recording publishes nothing.
func (s *Service) Record(ctx context.Context, e event.Event) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		head, err := s.repo.LockHead(ctx, e.Organization)
		if err != nil {
			return err
		}
		seen, err := s.repo.Exists(ctx, e.Organization, e.ID)
		if err != nil || seen {
			return err
		}
		entry, err := audit.FromEvent(e, head, s.opts.Now())
		if err != nil {
			return err
		}
		return s.repo.Append(ctx, entry)
	})
}

// List reads a page and names each entry's channel and actor. A channel
// filter is turned into the clients it selects (an unknown one is invalid).
func (s *Service) List(ctx context.Context, organization identity.OrganizationID, filter audit.Filter, page query.Pagination) (query.Paginated[audit.Entry], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[audit.Entry]{}, err
	}
	filter.Clients = nil
	if filter.Channel != "" {
		m, ok := channel.Filter(filter.Channel, s.opts.Assistants)
		if !ok {
			return query.Paginated[audit.Entry]{}, errx.InvalidField("channel",
				"channel must be console, github, platform, assistant, assistant:<key> or unrecorded")
		}
		filter.Clients = &m
	}
	result, err := s.repo.List(ctx, organization, filter, page.Normalize())
	if err != nil {
		return query.Paginated[audit.Entry]{}, err
	}
	names := s.names(ctx, organization)
	for i := range result.Items {
		e := &result.Items[i]
		e.Channel = channel.Of(e.Client, s.opts.Assistants)
		if !e.Actor.IsZero() {
			e.ActorName = names(e.Actor)
		}
	}
	return result, nil
}

// Summary counts the last SummaryDays: entries, people, and publications by
// channel (most first; the channel names, not the raw clients).
func (s *Service) Summary(ctx context.Context, organization identity.OrganizationID) (audit.Summary, error) {
	from := s.opts.Now().UTC().AddDate(0, 0, -audit.SummaryDays)
	tallies, people, err := s.repo.Tally(ctx, organization, from)
	if err != nil {
		return audit.Summary{}, err
	}
	sum := audit.Summary{From: from, People: people, ByChannel: []audit.ChannelCount{}}
	by := map[string]int64{}
	for _, t := range tallies {
		sum.Events += t.Entries
		if t.Type == deploymentStarted {
			sum.Deployments += t.Entries
			by[channel.Of(t.Client, s.opts.Assistants)] += t.Entries
		}
	}
	for ch, n := range by {
		sum.ByChannel = append(sum.ByChannel, audit.ChannelCount{Channel: ch, Count: n})
	}
	sort.Slice(sum.ByChannel, func(i, j int) bool {
		a, b := sum.ByChannel[i], sum.ByChannel[j]
		return a.Count > b.Count || (a.Count == b.Count && a.Channel < b.Channel)
	})
	return sum, nil
}

// People are who appear in the trail, named by the directory.
func (s *Service) People(ctx context.Context, organization identity.OrganizationID) ([]audit.Person, error) {
	people, err := s.repo.People(ctx, organization, peopleLimit)
	if err != nil {
		return nil, err
	}
	names := s.names(ctx, organization)
	for i := range people {
		people[i].Name = names(people[i].Actor)
	}
	return people, nil
}

// names looks people up in the directory. The trail stands without names: a
// directory that fails is logged once per call and leaves them empty (the
// console shows "Persona sin nombre"), never an error.
func (s *Service) names(ctx context.Context, organization identity.OrganizationID) func(identity.UserID) string {
	if s.opts.Directory == nil {
		return func(identity.UserID) string { return "" }
	}
	failed := false
	return func(user identity.UserID) string {
		if failed {
			return ""
		}
		name, err := s.opts.Directory.Name(ctx, organization, user)
		if err != nil {
			failed = true
			slog.WarnContext(ctx, "audit: the directory did not answer; names left empty", "organization", organization, "err", err)
			return ""
		}
		return name
	}
}

// Verify re-hashes the organization's whole chain and checks it reaches the
// recorded head, so removing the newest entries is detected too. The head is
// read first: entries appended during the walk lie beyond it and are fine.
func (s *Service) Verify(ctx context.Context, organization identity.OrganizationID) (audit.Verification, error) {
	head, err := s.repo.Head(ctx, organization)
	if err != nil {
		return audit.Verification{}, err
	}
	var v audit.Verification
	v.VerifiedAt = s.opts.Now().UTC()
	broken := func(seq int64) {
		if v.BrokenAt == nil {
			v.BrokenAt = &seq
		}
	}
	prev := ""
	err = s.repo.Walk(ctx, organization, func(e audit.Entry) error {
		v.Entries++
		hash, err := e.ComputeHash()
		if err != nil || e.Seq != v.Entries || e.PrevHash != prev || hash != e.Hash ||
			(e.Seq == head.Seq && e.Hash != head.Hash) {
			broken(e.Seq)
		}
		prev = e.Hash
		return nil
	})
	if err != nil {
		return audit.Verification{}, err
	}
	if v.Entries < head.Seq { // the tail was cut off
		broken(v.Entries + 1)
	}
	v.Valid = v.BrokenAt == nil
	return v, nil
}
