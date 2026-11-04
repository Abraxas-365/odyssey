package access

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/odiceaauth"
)

// The app-side library verifies what the gate signs, with the keys the gate
// serves (the same handler, behind an httptest server).
func TestAssertionVerifiesWithPkgOdiceaauth(t *testing.T) {
	f := newFixture(t)
	keys := httptest.NewServer(f.gate.Signer.Handler())
	defer keys.Close()
	c := cookieOf(f.callback(f.ticket(nil), "/"))
	tok := f.verifyReq("team", orgID, "/", c.Value).Header().Get(AssertionHeader)

	v := &odiceaauth.Verifier{JWKSURL: keys.URL + "/jwks", Issuer: AssertionIssuer, Audience: host, Now: func() time.Time { return f.now }}
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "user-1" || id.Email != "ana@x.test" || id.Name != "Ana" || id.Org != orgID || id.AppOrg != orgID || id.Env != envID || id.Mode != "team" {
		t.Errorf("identity: %+v", id)
	}
	// another app's audience, and an expired assertion, are refused
	other := &odiceaauth.Verifier{JWKSURL: keys.URL + "/jwks", Issuer: AssertionIssuer, Audience: "other.apps.test:8088", Now: v.Now}
	if _, err := other.Verify(context.Background(), tok); err == nil {
		t.Error("an assertion of app A opened app B")
	}
	late := &odiceaauth.Verifier{JWKSURL: keys.URL + "/jwks", Issuer: AssertionIssuer, Audience: host, Now: func() time.Time { return f.now.Add(10 * time.Minute) }}
	if _, err := late.Verify(context.Background(), tok); err == nil {
		t.Error("an expired assertion was accepted")
	}
}
