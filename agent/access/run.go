package access

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KeySecretKey is the key of the Secret that holds the cookie HMAC key.
const KeySecretKey = "cookie-key"

// Run is `odyssey-agent access`: it serves the gate until SIGINT/SIGTERM.
//
// Configuration (flag, then environment):
//
//	--issuer / ODYSSEY_ACCESS_ISSUER      the control plane's PUBLIC base URL: the iss claim and
//	                                      where browsers sign in (required)
//	--jwks-url / ODYSSEY_ACCESS_JWKS_URL  where the gate fetches the signing keys, when the cluster
//	                                      reaches the control plane by another address than the
//	                                      browser (default: <issuer>/.well-known/odicea-access-keys)
//	ODYSSEY_ACCESS_INSECURE_ISSUER=true   allow an http issuer / JWKS URL that is not local
//	                                      (https is required except for localhost, 127.0.0.1,
//	                                      *.localhost and host.k3d.internal)
//	--listen                              default :8098
//	--health-listen                       default :8097 (/healthz only: kubelet probes)
//	--secret / --secret-namespace         the cookie key's Secret (default odyssey-access in
//	                                      $POD_NAMESPACE or odyssey-system). The AGENT creates it
//	                                      (32 random bytes, EnsureKey); the gate only reads it, so
//	                                      its Role is a get on that one name
func Run(args []string) error {
	fs := flag.NewFlagSet("odyssey-agent access", flag.ContinueOnError)
	listen := fs.String("listen", env("ODYSSEY_ACCESS_LISTEN", ":8098"), "address of the gate")
	issuer := fs.String("issuer", os.Getenv("ODYSSEY_ACCESS_ISSUER"), "the control plane's public base URL (iss; where browsers sign in)")
	jwks := fs.String("jwks-url", os.Getenv("ODYSSEY_ACCESS_JWKS_URL"), "where to fetch the ticket keys (default <issuer>/.well-known/odicea-access-keys)")
	healthListen := fs.String("health-listen", env("ODYSSEY_ACCESS_HEALTH_LISTEN", ":8097"), "address of the liveness endpoint (the gate's port accepts only Traefik)")
	ns := fs.String("secret-namespace", env("POD_NAMESPACE", "odyssey-system"), "namespace of the cookie key's Secret")
	secret := fs.String("secret", env("ODYSSEY_ACCESS_SECRET", "odyssey-access"), "the Secret that holds the cookie key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*issuer = strings.TrimRight(*issuer, "/")
	if *issuer == "" {
		return errors.New("--issuer (ODYSSEY_ACCESS_ISSUER) is required")
	}
	if *jwks == "" {
		*jwks = *issuer + "/.well-known/odicea-access-keys"
	}
	insecure := os.Getenv("ODYSSEY_ACCESS_INSECURE_ISSUER") == "true"
	for name, u := range map[string]string{"issuer": *issuer, "jwks-url": *jwks} {
		if err := CheckURL(u, insecure); err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("the gate runs in the cluster: %w", err)
	}
	kc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	key, err := LoadKey(ctx, kc, *ns, *secret)
	if err != nil {
		return fmt.Errorf("cookie key: %w", err)
	}
	codec, err := NewCodec(key)
	if err != nil {
		return err
	}
	g := &Gate{Issuer: *issuer, Cookies: codec, Log: log, Verifier: &Verifier{Issuer: *issuer, JWKSURL: *jwks}}
	srv := &http.Server{
		Addr: *listen, Handler: g.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 32 << 10,
	}
	health := &http.Server{Addr: *healthListen, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
	go func() { _ = health.ListenAndServe() }()
	go func() {
		<-ctx.Done()
		_ = health.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("odyssey-agent access starting", "listen", *listen, "issuer", *issuer, "jwks", *jwks)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// EnsureKey creates the Secret that holds the cookie key, with 32 random
// bytes, unless it exists (an existing Secret is left as it is). The agent
// does it at start, so the gate itself needs no right to create Secrets:
// only to read its own by name.
func EnsureKey(ctx context.Context, kc kubernetes.Interface, namespace, name string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	_, err := kc.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/part-of": "odyssey"}},
		Data:       map[string][]byte{KeySecretKey: key},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// LoadKey reads the cookie key from the Secret, waiting for it to exist (the
// agent creates it) until ctx ends.
func LoadKey(ctx context.Context, kc kubernetes.Interface, namespace, name string) ([]byte, error) {
	for {
		s, err := kc.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			if len(s.Data[KeySecretKey]) < 32 {
				return nil, fmt.Errorf("secret %s/%s has no %s of 32 bytes or more", namespace, name, KeySecretKey)
			}
			return s.Data[KeySecretKey], nil
		case !apierrors.IsNotFound(err):
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("secret %s/%s does not exist: is the agent running with ODYSSEY_ACCESS_ENABLED?", namespace, name)
		case <-time.After(2 * time.Second):
		}
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// CheckURL requires u to be an absolute https URL, or http only for a local
// development address (localhost, 127.0.0.1, *.localhost, host.k3d.internal)
// or when insecure is true.
func CheckURL(u string, insecure bool) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.User != nil {
		return fmt.Errorf("%q is not an absolute URL", u)
	}
	switch p.Scheme {
	case "https":
		return nil
	case "http":
		h := p.Hostname()
		if insecure || h == "localhost" || h == "127.0.0.1" || h == "host.k3d.internal" || strings.HasSuffix(h, ".localhost") {
			return nil
		}
	}
	return fmt.Errorf("%q must be https (http is only for local addresses, or ODYSSEY_ACCESS_INSECURE_ISSUER=true)", u)
}
