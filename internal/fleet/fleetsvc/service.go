// Package fleetsvc implements the fleet ports: what platform people do with
// clusters, what other domains dispatch to them, and what the gateway does
// for connected agents.
package fleetsvc

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/event"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/txn"
)

// Options are the deployment-specific parts of the service.
type Options struct {
	// Gateway is the host:port agents dial (it goes into enrolment tokens).
	Gateway string
	// Now is the clock (tests pin it).
	Now func() time.Time
}

type Service struct {
	repo   fleet.Repository
	ca     fleet.Authority
	tx     txn.Runner
	events event.Publisher
	opts   Options
}

func New(repo fleet.Repository, ca fleet.Authority, tx txn.Runner, events event.Publisher, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{repo: repo, ca: ca, tx: tx, events: events, opts: opts}
}

var (
	_ fleet.Commands = (*Service)(nil)
	_ fleet.Queries  = (*Service)(nil)
	_ fleet.Dispatch = (*Service)(nil)
	_ fleet.Agents   = (*Service)(nil)
)

// ---- Commands ---------------------------------------------------------

func (s *Service) Create(ctx context.Context, organization identity.OrganizationID, input fleet.Create) (fleet.Created, error) {
	if err := input.Validate(); err != nil {
		return fleet.Created{}, err
	}
	c := fleet.Cluster{ID: fleet.NewClusterID(), Organization: organization, Name: input.Name,
		Status: fleet.StatusPending, CreatedBy: input.CreatedBy}
	token, hash, err := s.token(organization, c.ID)
	if err != nil {
		return fleet.Created{}, err
	}
	c.TokenExpiresAt = &token.ExpiresAt
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateCluster(ctx, c, hash); err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewClusterCreated(organization, input.CreatedBy, c))
	})
	if err != nil {
		return fleet.Created{}, err
	}
	created, err := s.repo.FindCluster(ctx, organization, c.ID)
	if err != nil {
		return fleet.Created{}, err
	}
	return fleet.Created{Cluster: created, Token: token}, nil
}

func (s *Service) IssueToken(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, input fleet.IssueToken) (fleet.Token, error) {
	if err := input.Validate(); err != nil {
		return fleet.Token{}, err
	}
	token, hash, err := s.token(organization, id)
	if err != nil {
		return fleet.Token{}, err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.FindClusterForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if c.Status == fleet.StatusRevoked {
			return errx.Business("Este clúster está desconectado para siempre. Registra uno nuevo.").WithCode(fleet.CodeClusterRevoked)
		}
		if err := s.repo.SetToken(ctx, organization, id, hash, token.ExpiresAt); err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewTokenIssued(organization, input.IssuedBy, c, token))
	})
	if err != nil {
		return fleet.Token{}, err
	}
	return token, nil
}

func (s *Service) Revoke(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, input fleet.Revoke) error {
	if err := input.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.FindClusterForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if c.Status == fleet.StatusRevoked {
			return nil // already revoked: nothing changes, nothing to record
		}
		if err := s.repo.Revoke(ctx, organization, id, input.RevokedBy); err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewRevoked(organization, input.RevokedBy, c))
	})
}

// token builds an enrolment token with what the agent needs to reach and
// trust the gateway.
func (s *Service) token(organization identity.OrganizationID, id fleet.ClusterID) (fleet.Token, []byte, error) {
	trust, err := s.ca.Trust()
	if err != nil {
		return fleet.Token{}, nil, err
	}
	value, hash := fleet.NewToken(organization, id)
	return fleet.Token{Value: value, ExpiresAt: s.opts.Now().Add(fleet.TokenTTL).UTC().Truncate(time.Second),
		Gateway: s.opts.Gateway, CAPin: trust.Pin}, hash, nil
}

// ---- Queries ----------------------------------------------------------

func (s *Service) Find(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID) (fleet.Cluster, error) {
	return s.repo.FindCluster(ctx, organization, id)
}

func (s *Service) List(ctx context.Context, organization identity.OrganizationID, filter fleet.Filter, page query.Pagination) (query.Paginated[fleet.Cluster], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[fleet.Cluster]{}, err
	}
	return s.repo.ListClusters(ctx, organization, filter, page.Normalize())
}

// ---- Dispatch ---------------------------------------------------------

// Apply places the environment on first use, then stores the app and
// signals its cluster's gateway, all in one transaction.
func (s *Service) Apply(ctx context.Context, app fleet.App) error {
	if err := app.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		placed := false
		cluster, err := s.repo.FindPlacement(ctx, app.Organization, app.Environment)
		if errx.IsType(err, errx.TypeNotFound) {
			def, err := s.repo.FindDefaultCluster(ctx, app.Organization)
			if errx.IsType(err, errx.TypeNotFound) {
				return errx.Business("Tu organización todavía no tiene un clúster conectado. Pide a TI que conecte uno en Infraestructura → Clústeres.").
					WithCode(fleet.CodeNoCluster)
			}
			if err != nil {
				return err
			}
			if cluster, err = s.repo.Place(ctx, app.Organization, app.Project, app.Environment, def.ID); err != nil {
				return err
			}
			placed = cluster == def.ID
		} else if err != nil {
			return err
		}
		c, err := s.repo.FindCluster(ctx, app.Organization, cluster)
		if err != nil {
			return err
		}
		if c.Status == fleet.StatusRevoked {
			// Revoked is final: the environment moves to the organization's
			// current default cluster (its apps are sent there from scratch).
			def, err := s.repo.FindDefaultCluster(ctx, app.Organization)
			if errx.IsType(err, errx.TypeNotFound) {
				return errx.Business("El clúster de este entorno fue desconectado y tu organización no tiene otro conectado. Pide a TI que conecte uno en Infraestructura → Clústeres.").
					WithCode(fleet.CodeClusterRevoked)
			}
			if err != nil {
				return err
			}
			if err := s.repo.MovePlacement(ctx, app.Organization, app.Environment, c.ID, def.ID); errx.IsType(err, errx.TypeNotFound) {
				// a concurrent deployment moved it first (the retry finds it there)
				return errx.Conflict("this environment was just moved to another cluster; retry").WithCode(fleet.CodePlacementMoved)
			} else if err != nil {
				return err
			}
			c, cluster, placed = def, def.ID, true
		}
		app.Cluster = cluster
		changed, err := s.repo.PutApp(ctx, app)
		if err != nil || !changed {
			return err
		}
		return s.events.Publish(ctx, fleet.NewAppApplied(app.Organization, c, app, placed))
	})
}

func (s *Service) Status(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID, name string) (fleet.AppStatus, error) {
	st, err := s.repo.FindAppStatus(ctx, organization, environment, name)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.AppStatus{Environment: environment, Name: name}, nil
	}
	return st, err
}

// ---- Agents -----------------------------------------------------------

// Enroll spends the token (once: concurrent enrolments race on the same
// row) and signs the CSR. A second enrolment of an active cluster (a lost
// key) invalidates its previous certificates.
func (s *Service) Enroll(ctx context.Context, token string, csr *x509.CertificateRequest) (fleet.Enrollment, error) {
	org, id, err := fleet.ParseToken(token)
	if err != nil {
		return fleet.Enrollment{}, err
	}
	if err := checkCSR(csr); err != nil {
		return fleet.Enrollment{}, err
	}
	var out fleet.Enrollment
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.ConsumeToken(ctx, org, id, fleet.HashToken(token))
		if errx.IsType(err, errx.TypeNotFound) {
			return fleet.InvalidToken()
		}
		if err != nil {
			return err
		}
		cert, err := s.ca.Sign(csr, fleet.AgentURI(org, id), s.opts.Now(), fleet.CertificateTTL)
		if err != nil {
			return err
		}
		if err := s.repo.SetCertificate(ctx, org, id, cert); err != nil {
			return err
		}
		out = fleet.Enrollment{Organization: org, Cluster: id, Certificate: cert}
		return s.events.Publish(ctx, fleet.NewEnrolled(org, c, cert))
	})
	return out, err
}

func (s *Service) Authenticate(ctx context.Context, agent fleet.AgentRef) error {
	serials, err := s.repo.FindAcceptedSerials(ctx, agent.Organization, agent.Cluster)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.Rejected()
	}
	if err != nil {
		return err
	}
	for _, serial := range serials {
		if serial == agent.Serial {
			return nil
		}
	}
	return fleet.Rejected()
}

// Renew issues a certificate for a new key to the holder of the current
// one or of the previous one (still accepted for RenewalOverlap: an agent
// whose last renewal answer was lost retries with it). The certificate
// presented stays accepted for RenewalOverlap, no longer, so a copy of an
// old key is soon useless. Renewing a young certificate is refused (a
// looping agent must not flood the CA and the audit trail): the current one
// before RenewAfter, the previous one before the current is RenewRetryAfter old.
func (s *Service) Renew(ctx context.Context, agent fleet.AgentRef, csr *x509.CertificateRequest) (fleet.Issued, error) {
	if err := checkCSR(csr); err != nil {
		return fleet.Issued{}, err
	}
	var out fleet.Issued
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.FindClusterForUpdate(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		if c.Status != fleet.StatusActive {
			return fleet.Rejected()
		}
		serials, err := s.repo.FindAcceptedSerials(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		if !contains(serials, agent.Serial) {
			return fleet.Rejected()
		}
		now := s.opts.Now()
		minAge := fleet.RenewAfter // renewing the current certificate
		if agent.Serial != c.Certificate.Serial {
			minAge = fleet.RenewRetryAfter // retrying with the previous one
		}
		if c.Certificate.ExpiresAt != nil && now.Before(c.Certificate.ExpiresAt.Add(-fleet.CertificateTTL+minAge)) {
			return errx.Business("this certificate is too young to renew").WithCode(fleet.CodeRenewTooEarly)
		}
		if out, err = s.ca.Sign(csr, fleet.AgentURI(agent.Organization, agent.Cluster), now, fleet.CertificateTTL); err != nil {
			return err
		}
		if err := s.repo.RenewCertificate(ctx, agent.Organization, agent.Cluster, out, agent.Serial, now.Add(fleet.RenewalOverlap)); err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewCertificateRenewed(agent.Organization, c, out))
	})
	return out, err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Connect records the tunnel if the certificate it was opened with is still
// accepted (checked under the row lock: a re-enrolment committed after the
// handshake wins).
func (s *Service) Connect(ctx context.Context, agent fleet.AgentRef, session fleet.Session, info fleet.Agent, address string) error {
	if err := info.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, err := s.repo.FindClusterForUpdate(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		if c.Status != fleet.StatusActive {
			return fleet.Rejected()
		}
		serials, err := s.repo.FindAcceptedSerials(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		if !contains(serials, agent.Serial) {
			return fleet.Rejected()
		}
		if err := s.repo.SetSession(ctx, agent.Organization, agent.Cluster, session, info); err != nil {
			return err
		}
		if c.LastSeenAt != nil && s.opts.Now().Sub(*c.LastSeenAt) < fleet.ReconnectQuiet && c.Agent.Version == info.Version {
			return nil // a reconnect, not news
		}
		return s.events.Publish(ctx, fleet.NewConnected(agent.Organization, c, info, address))
	})
}

func (s *Service) Heartbeat(ctx context.Context, agent fleet.AgentRef, session fleet.Session) error {
	ok, err := s.repo.ObserveHeartbeat(ctx, agent.Organization, agent.Cluster, session, agent.Serial)
	if err != nil {
		return err
	}
	if !ok {
		return s.lost(ctx, agent)
	}
	return nil
}

// lost is why a tunnel's write found it no longer the cluster's: its
// certificate is not accepted (revoked, re-enrolled) or another tunnel
// took over.
func (s *Service) lost(ctx context.Context, agent fleet.AgentRef) error {
	serials, err := s.repo.FindAcceptedSerials(ctx, agent.Organization, agent.Cluster)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.Rejected()
	}
	if err != nil {
		return err
	}
	if !contains(serials, agent.Serial) {
		return fleet.Rejected()
	}
	return fleet.Superseded()
}

func (s *Service) Disconnect(ctx context.Context, agent fleet.AgentRef, session fleet.Session) error {
	return s.repo.ObserveDisconnect(ctx, agent.Organization, agent.Cluster, session)
}

func (s *Service) Desired(ctx context.Context, agent fleet.AgentRef) ([]fleet.App, error) {
	return s.repo.ListApps(ctx, agent.Organization, agent.Cluster)
}

func (s *Service) DesiredApp(ctx context.Context, agent fleet.AgentRef, environment project.EnvironmentID, name string) (fleet.App, bool, error) {
	a, err := s.repo.FindApp(ctx, agent.Organization, agent.Cluster, environment, name)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.App{}, false, nil
	}
	return a, err == nil, err
}

// Report stores what the agent observed. An invalid app status is dropped
// (and the report answered with a validation error naming how many) while
// the valid ones in the same report are kept: one bad app must not hide
// the others' state.
func (s *Service) Report(ctx context.Context, agent fleet.AgentRef, session fleet.Session, report fleet.Report) error {
	if err := report.Validate(); err != nil {
		return err
	}
	valid, invalid := report.Valid()
	live, err := s.repo.ObserveApps(ctx, agent.Organization, agent.Cluster, session, valid)
	if err != nil {
		return err
	}
	if !live {
		return s.lost(ctx, agent) // a superseded or revoked tunnel writes nothing
	}
	return invalid
}

// checkCSR accepts only a signed request for an ECDSA P-256 or Ed25519 key
// (what the agent generates). Everything else in it — subject, SANs,
// extensions — is ignored on purpose: the certificate names the cluster of
// the token (fleetca.Sign builds it from scratch), so an agent cannot ask
// for another identity.
func checkCSR(csr *x509.CertificateRequest) error {
	if csr == nil {
		return errx.Validation("a certificate request is required")
	}
	if len(csr.Raw) > fleet.MaxCSR {
		return errx.Validation("the certificate request is too large")
	}
	if err := csr.CheckSignature(); err != nil {
		return errx.Validation("the certificate request signature is not valid")
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return nil
		}
	case ed25519.PublicKey:
		return nil
	}
	return errx.Validation("the certificate request must be for an ECDSA P-256 or Ed25519 key")
}
