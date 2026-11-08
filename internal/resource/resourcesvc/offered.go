package resourcesvc

import (
	"context"
	"slices"

	"github.com/Abraxas-365/odyssey/internal/catalog"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/gate"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/resource"
	"github.com/Abraxas-365/odyssey/pkg/resourcekind"
)

func (s *Service) Offered(ctx context.Context, org identity.OrganizationID, id project.ID, env project.EnvironmentID, asker identity.UserID) (resource.Offered, error) {
	p, e, err := s.place(ctx, org, id, env)
	if err != nil {
		return resource.Offered{}, err
	}
	if p.Archived() { // Create refuses it: offer nothing
		return resource.Offered{}, project.ArchivedError()
	}
	caps, err := s.cluster.Capabilities(ctx, org, e.ID)
	if err != nil {
		return resource.Offered{}, err
	}
	entries, err := s.catalog.List(ctx, org)
	if err != nil {
		return resource.Offered{}, err
	}
	// the asker's teams, as Resolve reads them when they create
	teams, err := s.catalog.TeamsOf(ctx, org, asker)
	if err != nil {
		return resource.Offered{}, err
	}
	out := resource.Offered{Tier: p.Tier, Recipes: []resource.Offer{}, Unavailable: []resource.UnavailableKind{}}
	for _, k := range resourcekind.Kinds {
		// a kind no recipe of the catalog prepares (object storage until IT
		// publishes one) is not something to ask IT for
		if !slices.ContainsFunc(entries, func(e catalog.Entry) bool { return e.Kind == k }) {
			continue
		}
		var offers []resource.Offer
		reason := ""
		// the default Resolve picks for this asker (a team's over everyone's;
		// none when several of their teams have one)
		var mine []catalog.Entry
		for _, entry := range entries {
			if entry.Kind == k && entry.Offering.OffersTo(p.Tier, teams) {
				mine = append(mine, entry)
			}
		}
		def, hasDefault := catalog.DefaultAmong(mine, p.Tier)
		for _, entry := range entries {
			if entry.Kind != k {
				continue
			}
			if err := entry.Usable(p.Tier, caps, teams); err != nil {
				// the cluster's reason (or the teams') says more than "not
				// offered" when the tier does offer the kind
				if entry.Offering.Offers(p.Tier) || reason == "" {
					reason = messageOf(err)
				}
				continue
			}
			rv, v, err := s.verdictOnDefaults(ctx, p, e, entry)
			if err != nil {
				return resource.Offered{}, err
			}
			o := resource.NewOffer(entry, p.Tier, v, rv)
			o.Default = hasDefault && entry.Recipe == def.Recipe
			if o.Default {
				offers = append([]resource.Offer{o}, offers...)
			} else {
				offers = append(offers, o)
			}
		}
		if len(offers) > 0 {
			out.Recipes = append(out.Recipes, offers...)
			continue
		}
		switch {
		case len(caps) == 0:
			reason = resource.NoClusterReason
		case reason == "":
			reason = messageOf(catalog.Unavailable(k, p.Tier))
		}
		out.Unavailable = append(out.Unavailable, resource.UnavailableKind{Kind: k, Reason: reason})
	}
	return out, nil
}

// verdictOnDefaults is what create_resource's default gets: entry with its
// defaults, connected to the whole app, judged as Create judges it (IT's
// rules under create_resource, the catalog's floor and the rules on
// connecting): all of it, and the rules alone (without the floor, which
// depends on the values asked). A connection the app's shape refuses (a
// files volume in an app of several services must name one) is judged
// without it: the request will name the service, and the rules on that one
// are asked then.
func (s *Service) verdictOnDefaults(ctx context.Context, p project.Project, e project.Environment, entry catalog.Entry) (rules, all gate.Verdict, err error) {
	params, outside, err := catalog.Check(entry.RecipeVersion, entry.Offering, nil)
	if err != nil {
		return gate.Verdict{}, gate.Verdict{}, err
	}
	r := catalog.Resolved{Kind: entry.Kind, Recipe: entry.Recipe, Version: entry.Version, Engine: entry.Engine, Title: entry.Title, Params: params,
		Spec: entry.Spec, OutOfRange: outside}
	var others []gate.Verdict
	cv, err := s.usage.JudgeConnect(ctx, p.Organization, p.ID, e.ID, resource.Ask{Kind: entry.Kind})
	var invalid *errx.Error
	switch {
	case err == nil:
		others = append(others, cv)
	case !errx.As(err, &invalid) || invalid.Type != errx.TypeValidation || invalid.Details["field"] != "services":
		return gate.Verdict{}, gate.Verdict{}, err
	}
	rules, err = s.judge(ctx, p, e, gate.Request{Action: gate.ActionCreateResource, ResourceKind: string(entry.Kind), Recipe: r.Gate()}, others...)
	if err != nil {
		return gate.Verdict{}, gate.Verdict{}, err
	}
	return rules, rules.With(catalog.Floor(r)), nil
}

func messageOf(err error) string {
	var e *errx.Error
	if errx.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
