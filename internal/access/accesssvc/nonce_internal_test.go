package accesssvc

import (
	"context"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/access"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

type countingLogin struct{ exchanges int }

func (l *countingLogin) AuthorizeURL(access.LoginRequest) string { return "" }
func (l *countingLogin) Exchange(context.Context, string, string, string, string) (access.Identity, error) {
	l.exchanges++
	return access.Identity{Subject: "u"}, nil
}

// TestAFlowWithoutANonceNeverReachesIAMKit: a flow sealed by an older build
// (or any flow) without a nonce cannot bind the ID token, so it is refused
// before the code is exchanged.
func TestAFlowWithoutANonceNeverReachesIAMKit(t *testing.T) {
	login := &countingLogin{}
	now := time.Unix(1_800_000_000, 0)
	s, err := New(nil, nil, login, []byte("secret"), Options{Issuer: "https://api.example", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.seal(flow{Environment: "e", Mode: "team", Return: "https://app.example/", Verifier: "v", State: "st",
		Expires: now.Add(access.FlowTTL).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Callback(context.Background(), access.Callback{Flow: sealed, State: "st", Code: "c"})
	var e *errx.Error
	if !errx.As(err, &e) || e.Code != access.CodeFlowInvalid || login.exchanges != 0 {
		t.Fatalf("err %v, exchanges %d", err, login.exchanges)
	}
}
