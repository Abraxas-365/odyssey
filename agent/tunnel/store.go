package tunnel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Store keeps the agent's enrolment input and identity.
type Store interface {
	// Load returns the identity (zero before enrolling) and the pending
	// enrolment: its token (when there is one) and the pending key (when an
	// answer may have been lost).
	Load(ctx context.Context) (Identity, Enrollment, error)
	// Save keeps the identity and drops the token (it is spent) and the
	// pending key (it is the identity's now, or moot).
	Save(ctx context.Context, id Identity) error
	// SavePending keeps the key of an Enroll or Renew about to be sent (nil
	// drops it): if the answer is lost, Recover gets its certificate back.
	SavePending(ctx context.Context, keyPEM []byte) error
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
	KeyPending      = "pending.key"
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
	return id, Enrollment{Token: str(KeyToken), Gateway: str(KeyGateway), CAPin: str(KeyCAPin), PendingKey: d[KeyPending]}, nil
}

func (s *SecretStore) Save(ctx context.Context, id Identity) error {
	return s.update(ctx, "save the agent identity", func(data map[string][]byte) {
		delete(data, KeyToken)
		delete(data, KeyPending)
		data[KeyKey], data[KeyCert], data[KeyCA] = id.KeyPEM, id.CertPEM, id.CAPEM
		data[KeyOrganization], data[KeyCluster] = []byte(id.Organization), []byte(id.Cluster)
		data[KeyGateway], data[KeyCAPin] = []byte(id.Gateway), []byte(id.CAPin)
	})
}

func (s *SecretStore) SavePending(ctx context.Context, keyPEM []byte) error {
	return s.update(ctx, "save the pending key", func(data map[string][]byte) {
		if keyPEM == nil {
			delete(data, KeyPending)
		} else {
			data[KeyPending] = keyPEM
		}
	})
}

func (s *SecretStore) update(ctx context.Context, what string, change func(map[string][]byte)) error {
	var sec corev1.Secret
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &sec); err != nil {
		return fmt.Errorf("read secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	change(sec.Data)
	if err := s.Writer.Update(ctx, &sec); err != nil {
		return fmt.Errorf("%s in secret %s/%s: %w", what, s.Namespace, s.Name, err)
	}
	return nil
}

// MemoryStore keeps everything in memory (tests).
type MemoryStore struct {
	mu         sync.Mutex
	identity   Identity
	enrollment Enrollment
	// LoseAnswers makes the next n Save calls fail (tests: an answer the
	// agent got but could not keep is as good as lost).
	LoseAnswers int
	lost        int
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
	if m.LoseAnswers > 0 {
		m.LoseAnswers--
		m.lost++
		return errors.New("lost (test)")
	}
	m.identity, m.enrollment.Token, m.enrollment.PendingKey = id, "", nil
	return nil
}

func (m *MemoryStore) SavePending(_ context.Context, keyPEM []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enrollment.PendingKey = keyPEM
	return nil
}

// Lose makes the next n Save calls fail.
func (m *MemoryStore) Lose(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LoseAnswers = n
}

// Lost is how many Save calls failed on purpose.
func (m *MemoryStore) Lost() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lost
}

// Pending is the pending key (nil when there is none).
func (m *MemoryStore) Pending() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enrollment.PendingKey
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
