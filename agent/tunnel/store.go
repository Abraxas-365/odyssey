package tunnel

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Store keeps the agent's enrolment input and identity.
type Store interface {
	// Load returns the identity (zero before enrolling) and a pending
	// enrolment (zero when there is none).
	Load(ctx context.Context) (Identity, Enrollment, error)
	// Save keeps the identity and drops the pending enrolment's token (it is
	// spent).
	Save(ctx context.Context, id Identity) error
}

// Keys of the agent's Secret. An operator creates it with token, gateway
// and ca-pin (the install command does); the agent adds its identity and
// removes the token once spent. Writing a new token re-enrols the cluster.
const (
	KeyToken        = "token"
	KeyGateway      = "gateway"
	KeyCAPin        = "ca-pin"
	KeyKey          = "tls.key"
	KeyCert         = "tls.crt"
	KeyCA           = "ca.crt"
	KeyOrganization = "organization"
	KeyCluster      = "cluster"
)

// +kubebuilder:rbac:groups="",namespace=odyssey-system,resources=secrets,resourceNames=odyssey-agent,verbs=get;update

// SecretStore keeps everything in one Secret the agent may read and update
// but not create (nor see any other Secret).
type SecretStore struct {
	// Reader must read the API directly (the agent caches no Secrets).
	Reader    client.Reader
	Writer    client.Writer
	Namespace string
	Name      string
}

func (s *SecretStore) Load(ctx context.Context) (Identity, Enrollment, error) {
	var sec corev1.Secret
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return Identity{}, Enrollment{}, fmt.Errorf("secret %s/%s does not exist: install the agent with the command the console shows", s.Namespace, s.Name)
		}
		return Identity{}, Enrollment{}, fmt.Errorf("read secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	d := sec.Data
	str := func(k string) string { return string(d[k]) }
	id := Identity{
		Organization: str(KeyOrganization), Cluster: str(KeyCluster), KeyPEM: d[KeyKey], CertPEM: d[KeyCert], CAPEM: d[KeyCA],
		Gateway: str(KeyGateway), CAPin: str(KeyCAPin),
	}
	if len(id.CertPEM) == 0 || len(id.KeyPEM) == 0 {
		id = Identity{}
	}
	return id, Enrollment{Token: str(KeyToken), Gateway: str(KeyGateway), CAPin: str(KeyCAPin)}, nil
}

func (s *SecretStore) Save(ctx context.Context, id Identity) error {
	var sec corev1.Secret
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &sec); err != nil {
		return fmt.Errorf("read secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	delete(sec.Data, KeyToken)
	delete(sec.StringData, KeyToken)
	sec.Data[KeyKey], sec.Data[KeyCert], sec.Data[KeyCA] = id.KeyPEM, id.CertPEM, id.CAPEM
	sec.Data[KeyOrganization], sec.Data[KeyCluster] = []byte(id.Organization), []byte(id.Cluster)
	sec.Data[KeyGateway], sec.Data[KeyCAPin] = []byte(id.Gateway), []byte(id.CAPin)
	if err := s.Writer.Update(ctx, &sec); err != nil {
		return fmt.Errorf("save the agent identity in secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return nil
}

// MemoryStore keeps everything in memory (tests).
type MemoryStore struct {
	mu         sync.Mutex
	identity   Identity
	enrollment Enrollment
}

// NewMemoryStore starts with a pending enrolment.
func NewMemoryStore(e Enrollment) *MemoryStore { return &MemoryStore{enrollment: e} }

func (m *MemoryStore) Load(context.Context) (Identity, Enrollment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.identity, m.enrollment, nil
}

func (m *MemoryStore) Save(_ context.Context, id Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identity, m.enrollment.Token = id, ""
	return nil
}

// Enroll sets a new pending enrolment (an operator writing a new token).
func (m *MemoryStore) Enroll(e Enrollment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enrollment = e
}

// Identity is what was saved last.
func (m *MemoryStore) Identity() Identity {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.identity
}
