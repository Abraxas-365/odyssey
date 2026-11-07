// Package fleetsvc implements the fleet ports: what platform people do with
// clusters, what other domains dispatch to them, and what the gateway does
// for connected agents.
package fleetsvc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
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
	// CertificateTTL is the agents' certificate lifetime (default
	// fleet.CertificateTTL; tests shorten it to see real renewals). A
	// renewal is refused before half of it has passed.
	CertificateTTL time.Duration
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
	if opts.CertificateTTL <= 0 {
		opts.CertificateTTL = fleet.CertificateTTL
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
		if err := s.lockTargetFirst(ctx, organization, id, input.MoveTo); err != nil {
			return err
		}
		c, err := s.repo.FindClusterForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if c.Status != fleet.StatusRevoked { // already revoked: nothing to record
			if err := s.repo.Revoke(ctx, organization, id, input.RevokedBy); err != nil {
				return err
			}
			if err := s.events.Publish(ctx, fleet.NewRevoked(organization, input.RevokedBy, c)); err != nil {
				return err
			}
			// values sealed to its key can be opened by no other cluster
			dropped, err := s.repo.DropSecretDeliveries(ctx, organization, id)
			if err != nil {
				return err
			}
			for _, d := range dropped {
				e, err := s.settle(ctx, d, "el clúster fue revocado antes de recibirlo")
				if err != nil {
					return err
				}
				if err := s.events.Publish(ctx, e); err != nil {
					return err
				}
			}
			c.Status = fleet.StatusRevoked
		}
		if input.MoveTo == nil {
			return nil
		}
		_, err = s.move(ctx, c, *input.MoveTo, input.RevokedBy, "move_to")
		return err
	})
}

// Move sends every app of a revoked cluster to an active one.
func (s *Service) Move(ctx context.Context, organization identity.OrganizationID, id fleet.ClusterID, input fleet.Move) (fleet.Moved, error) {
	if err := input.Validate(); err != nil {
		return fleet.Moved{}, err
	}
	var moved fleet.Moved
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.lockTargetFirst(ctx, organization, id, &input.To); err != nil {
			return err
		}
		c, err := s.repo.FindClusterForUpdate(ctx, organization, id)
		if err != nil {
			return err
		}
		if c.Status != fleet.StatusRevoked {
			return errx.Business("Solo se mueven las apps de un clúster revocado: revócalo primero.").WithCode(fleet.CodeClusterNotRevoked)
		}
		moved, err = s.move(ctx, c, input.To, input.MovedBy, "to")
		return err
	})
	return moved, err
}

// lockTargetFirst locks the target of a move before its source when its id
// sorts first: both clusters are always locked in id order, so two crossed
// moves (A to B, B to A) wait for each other instead of deadlocking. A
// missing target is move's to report.
func (s *Service) lockTargetFirst(ctx context.Context, organization identity.OrganizationID, from fleet.ClusterID, to *fleet.ClusterID) error {
	if to == nil || to.String() >= from.String() {
		return nil
	}
	if _, err := s.repo.FindClusterForUpdate(ctx, organization, *to); err != nil && !errx.IsType(err, errx.TypeNotFound) {
		return err
	}
	return nil
}

// move pins every environment of the revoked cluster from to the active
// cluster to (locked, so it cannot be revoked meanwhile) and records it.
// An environment a concurrent deployment already moved is skipped. field
// names the request field that carried to.
func (s *Service) move(ctx context.Context, from fleet.Cluster, to fleet.ClusterID, actor identity.UserID, field string) (fleet.Moved, error) {
	target, err := s.repo.FindClusterForUpdate(ctx, from.Organization, to)
	if err != nil && !errx.IsType(err, errx.TypeNotFound) {
		return fleet.Moved{}, err
	}
	if err != nil || target.ID == from.ID || target.Status != fleet.StatusActive {
		return fleet.Moved{}, errx.Business("Las apps solo pueden ir a otro clúster conectado de tu organización.").
			WithCode(fleet.CodeInvalidMoveTarget).WithField(field)
	}
	envs, err := s.repo.ListPlacements(ctx, from.Organization, from.ID)
	if err != nil {
		return fleet.Moved{}, err
	}
	moved := fleet.Moved{Environments: []project.EnvironmentID{}}
	for _, env := range envs {
		n, err := s.repo.MovePlacement(ctx, from.Organization, env, from.ID, target.ID)
		if errx.IsType(err, errx.TypeNotFound) {
			continue
		}
		if err != nil {
			return fleet.Moved{}, err
		}
		moved.Environments, moved.Apps = append(moved.Environments, env), moved.Apps+n
	}
	if len(moved.Environments) == 0 {
		return moved, nil // nothing was there: nothing changed
	}
	return moved, s.events.Publish(ctx, fleet.NewAppsMoved(from.Organization, actor, from, target, moved))
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

func (s *Service) Placements(ctx context.Context, organization identity.OrganizationID, envs []project.EnvironmentID) (map[project.EnvironmentID]fleet.ClusterID, error) {
	if len(envs) == 0 {
		return map[project.EnvironmentID]fleet.ClusterID{}, nil
	}
	if len(envs) > fleet.MaxPlacements {
		return nil, errx.Validation("too many environments in one read")
	}
	return s.repo.ListPlacementsOf(ctx, organization, envs)
}

func (s *Service) List(ctx context.Context, organization identity.OrganizationID, filter fleet.Filter, page query.Pagination) (query.Paginated[fleet.Cluster], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[fleet.Cluster]{}, err
	}
	return s.repo.ListClusters(ctx, organization, filter, page.Normalize())
}

// ---- Dispatch ---------------------------------------------------------

// Apply places the environment on first use, then stores the app and
// signals its cluster's gateway, all in one transaction. The cluster is
// locked against revocation and moves (FOR KEY SHARE) until it commits, so
// an app is never stored for a cluster its environment just left. An
// environment on a revoked cluster stays there until TI moves its apps:
// nothing moves on its own (the other cluster may be on another network or
// far from the apps' data).
func (s *Service) Apply(ctx context.Context, app fleet.App) error {
	if err := app.Validate(); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, placed, err := s.placement(ctx, app.Organization, app.Project, app.Environment)
		if err != nil {
			return err
		}
		// An agent that cannot protect apps would serve them open.
		if (app.Access == fleet.AccessTeam || app.Access == fleet.AccessOrganization) && !c.Agent.Capabilities.CanProtect() {
			return errx.Business(fleet.AccessUnavailableMessage).WithCode(fleet.CodeAccessUnavailable)
		}
		// One that cannot hold its secrets would wait for them forever.
		if err := c.Agent.Capabilities.CheckSecrets(app.Secrets); err != nil {
			return err
		}
		// One that cannot provision its resources would wait for them forever.
		if err := c.Agent.Capabilities.CheckResources(app.Resources); err != nil {
			return err
		}
		// One that cannot wire the AI would leave the app without a key.
		if err := c.Agent.Capabilities.CheckAI(app.AI); err != nil {
			return err
		}
		// One that runs a single workload would serve half of the app.
		if !app.Simple() && !c.Agent.Capabilities.CanRunServices() {
			return errx.Business(project.ServicesUnavailableMessage).WithCode(project.CodeServicesUnavailable)
		}
		// One that ignores a setting's services would hand it to all of them.
		if app.ScopedSettings() && !c.Agent.Capabilities.CanScopeSettings() {
			return errx.Business(fleet.ServiceSettingsUnavailableMessage).WithCode(fleet.CodeServiceSettingsUnavailable)
		}
		app.Cluster = c.ID
		changed, err := s.repo.PutApp(ctx, app)
		if err != nil || !changed {
			return err
		}
		return s.events.Publish(ctx, fleet.NewAppApplied(app.Organization, c, app, placed))
	})
}

// Retire answers fleet.Dispatch.Retire. The row lock of the app orders it
// with deployments and moves (both write the same row); the cluster is the
// one the app is stored for, revoked or not (moved, the tombstone moves with
// it). The gateway holds it back from an agent without CapabilityRetire.
func (s *Service) Retire(ctx context.Context, r fleet.Retirement) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	retired := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		a, changed, err := s.repo.RetireApp(ctx, r)
		if err != nil || !changed {
			return err
		}
		c, err := s.repo.FindCluster(ctx, r.Organization, a.Cluster)
		if err != nil {
			return err
		}
		retired = true
		return s.events.Publish(ctx, fleet.NewAppRetired(r.Organization, c, r))
	})
	return retired, err
}

// placement is the active cluster of an environment, placing it on the
// organization's default cluster on first use (placed). It runs in the
// caller's transaction and locks the cluster against revocation and moves
// until it commits.
func (s *Service) placement(ctx context.Context, organization identity.OrganizationID, p project.ID, environment project.EnvironmentID) (fleet.Cluster, bool, error) {
	placed := false
	cluster, err := s.repo.FindPlacement(ctx, organization, environment)
	if errx.IsType(err, errx.TypeNotFound) {
		def, err := s.repo.FindDefaultCluster(ctx, organization)
		if errx.IsType(err, errx.TypeNotFound) {
			return fleet.Cluster{}, false, errx.Business("Tu organización todavía no tiene un clúster conectado. Pide a TI que conecte uno en Infraestructura → Clústeres.").
				WithCode(fleet.CodeNoCluster)
		}
		if err != nil {
			return fleet.Cluster{}, false, err
		}
		if cluster, err = s.repo.Place(ctx, organization, p, environment, def.ID); err != nil {
			return fleet.Cluster{}, false, err
		}
		placed = cluster == def.ID
	} else if err != nil {
		return fleet.Cluster{}, false, err
	}
	c, err := s.repo.FindClusterForShare(ctx, organization, cluster)
	if err != nil {
		return fleet.Cluster{}, false, err
	}
	// moved between the read and the lock: the retry finds it there
	if now, err := s.repo.FindPlacement(ctx, organization, environment); err != nil {
		return fleet.Cluster{}, false, err
	} else if now != cluster {
		return fleet.Cluster{}, false, errx.Conflict("this environment was just moved to another cluster; retry").WithCode(fleet.CodePlacementMoved)
	}
	if c.Status == fleet.StatusRevoked {
		if placed { // placed this instant on a cluster revoked meanwhile: the retry picks another
			return fleet.Cluster{}, false, errx.Conflict("this environment's cluster was just revoked; retry").WithCode(fleet.CodePlacementMoved)
		}
		return fleet.Cluster{}, false, errx.Business("El clúster de este entorno fue desconectado. Pide a TI que mueva sus apps a otro clúster en Infraestructura → Clústeres.").
			WithCode(fleet.CodeClusterRevoked)
	}
	return c, placed, nil
}

// CanProtect answers fleet.Dispatch.CanProtect without placing anything.
func (s *Service) CanProtect(ctx context.Context, organization identity.OrganizationID, environment project.EnvironmentID) (bool, error) {
	id, err := s.repo.FindPlacement(ctx, organization, environment)
	if errx.IsType(err, errx.TypeNotFound) {
		def, err := s.repo.FindDefaultCluster(ctx, organization)
		if errx.IsType(err, errx.TypeNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return def.Agent.Capabilities.CanProtect(), nil
	}
	if err != nil {
		return false, err
	}
	c, err := s.repo.FindCluster(ctx, organization, id)
	if err != nil {
		return false, err
	}
	return c.Status != fleet.StatusRevoked && c.Agent.Capabilities.CanProtect(), nil
}

// CanRunServices answers fleet.Dispatch.CanRunServices without placing
// anything: one read of the placements, one of each cluster involved.
func (s *Service) CanRunServices(ctx context.Context, organization identity.OrganizationID, environments []project.EnvironmentID) (bool, error) {
	if len(environments) == 0 {
		return false, nil
	}
	if len(environments) > fleet.MaxPlacements {
		return false, errx.Validation("too many environments in one read")
	}
	placed, err := s.repo.ListPlacementsOf(ctx, organization, environments)
	if err != nil {
		return false, err
	}
	clusters := map[fleet.ClusterID]bool{}
	for _, e := range environments {
		id, ok := placed[e]
		if !ok {
			def, err := s.repo.FindDefaultCluster(ctx, organization)
			if errx.IsType(err, errx.TypeNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			id = def.ID
		}
		clusters[id] = true
	}
	for id := range clusters {
		c, err := s.repo.FindCluster(ctx, organization, id)
		if err != nil {
			return false, err
		}
		if c.Status == fleet.StatusRevoked || !c.Agent.Capabilities.CanRunServices() {
			return false, nil
		}
	}
	return true, nil
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
		cert, err := s.ca.Sign(csr, fleet.AgentURI(org, id), s.opts.Now(), s.opts.CertificateTTL)
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
		now, ttl := s.opts.Now(), s.opts.CertificateTTL
		minAge := ttl / 2 // renewing the current certificate (fleet.RenewAfter by default)
		if agent.Serial != c.Certificate.Serial {
			minAge = min(fleet.RenewRetryAfter, ttl/10) // retrying with the previous one
		}
		if c.Certificate.ExpiresAt != nil && now.Before(c.Certificate.ExpiresAt.Add(-ttl+minAge)) {
			return errx.Business("this certificate is too young to renew").WithCode(fleet.CodeRenewTooEarly)
		}
		if out, err = s.ca.Sign(csr, fleet.AgentURI(agent.Organization, agent.Cluster), now, ttl); err != nil {
			return err
		}
		if err := s.repo.RenewCertificate(ctx, agent.Organization, agent.Cluster, out, agent.Serial, now.Add(fleet.RenewalOverlap)); err != nil {
			return err
		}
		return s.events.Publish(ctx, fleet.NewCertificateRenewed(agent.Organization, c, out))
	})
	return out, err
}

// Recover hands back the cluster's current certificate when it is for the
// key that signed csr. A certificate is public; the proof only keeps the
// answer (serials, expiry) from whoever merely names a cluster. Nothing is
// signed or changed, so there is nothing to record either.
func (s *Service) Recover(ctx context.Context, input fleet.Recover, csr *x509.CertificateRequest) (fleet.Enrollment, error) {
	if err := input.Validate(); err != nil {
		return fleet.Enrollment{}, err
	}
	if err := checkCSR(csr); err != nil {
		return fleet.Enrollment{}, err
	}
	org, id, _ := input.Target()
	raw, err := s.repo.FindCertificate(ctx, org, id)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.Enrollment{}, fleet.NothingToRecover()
	}
	if err != nil {
		return fleet.Enrollment{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return fleet.Enrollment{}, errx.Internal("the stored cluster certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fleet.Enrollment{}, errx.Wrap(err, "parse the stored cluster certificate", errx.TypeInternal)
	}
	pub, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(csr.PublicKey) || !s.opts.Now().Before(cert.NotAfter) {
		return fleet.Enrollment{}, fleet.NothingToRecover()
	}
	trust, err := s.ca.Trust()
	if err != nil {
		return fleet.Enrollment{}, err
	}
	return fleet.Enrollment{Organization: org, Cluster: id, Certificate: fleet.Issued{
		PEM: raw, CAPEM: trust.CAPEM, Serial: fleet.Serial(cert.SerialNumber), ExpiresAt: cert.NotAfter}}, nil
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

// Desired leaves out retirements an agent without CapabilityRetire would not
// carry out (it would run the app again): they wait, stored, for its update.
func (s *Service) Desired(ctx context.Context, agent fleet.AgentRef) ([]fleet.App, error) {
	apps, err := s.repo.ListApps(ctx, agent.Organization, agent.Cluster)
	if err != nil {
		return nil, err
	}
	canRetire, err := s.canRetire(ctx, agent)
	if err != nil {
		return nil, err
	}
	out := apps[:0]
	for _, a := range apps {
		if !a.Deleted || canRetire {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Service) DesiredApp(ctx context.Context, agent fleet.AgentRef, environment project.EnvironmentID, name string) (fleet.App, bool, error) {
	a, err := s.repo.FindApp(ctx, agent.Organization, agent.Cluster, environment, name)
	if errx.IsType(err, errx.TypeNotFound) {
		return fleet.App{}, false, nil
	}
	if err != nil {
		return fleet.App{}, false, err
	}
	if a.Deleted {
		if ok, err := s.canRetire(ctx, agent); err != nil || !ok {
			return fleet.App{}, false, err
		}
	}
	return a, true, nil
}

func (s *Service) canRetire(ctx context.Context, agent fleet.AgentRef) (bool, error) {
	c, err := s.repo.FindCluster(ctx, agent.Organization, agent.Cluster)
	if err != nil {
		return false, err
	}
	return c.Agent.Capabilities.CanRetire(), nil
}

// Report stores what the agent observed. An invalid app status is dropped
// (and the report answered with a validation error naming how many) while
// the valid ones in the same report are kept: one bad app must not hide
// the others' state. Retired apps reported gone are forgotten and published
// (cluster.app_removed) in the same transaction.
func (s *Service) Report(ctx context.Context, agent fleet.AgentRef, session fleet.Session, report fleet.Report) error {
	if err := report.Validate(); err != nil {
		return err
	}
	valid, invalid := report.Valid()
	live := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		ok, removed, err := s.repo.ObserveApps(ctx, agent.Organization, agent.Cluster, session, valid)
		if err != nil || !ok {
			return err
		}
		live = true
		if len(removed) == 0 {
			return nil
		}
		c, err := s.repo.FindCluster(ctx, agent.Organization, agent.Cluster)
		if err != nil {
			return err
		}
		for _, a := range removed {
			if err := s.events.Publish(ctx, fleet.NewAppRemoved(agent.Organization, c, a)); err != nil {
				return err
			}
		}
		return nil
	})
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
