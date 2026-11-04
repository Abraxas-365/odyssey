package accessmodule

import "testing"

// TestTheBackChannelIsTLSOrPrivate: the ID token is trusted for coming from
// IAMKit's token endpoint, so plain http is refused on a public host.
func TestTheBackChannelIsTLSOrPrivate(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://iam.example.com":                  true,
		"http://iamkit:8080":                       true, // compose / same namespace
		"http://localhost:8085":                    true,
		"http://iamkit.iam.svc:8080":               true,
		"http://iamkit.iam.svc.cluster.local:8080": true,
		"http://host.k3d.internal:8085":            true,
		"http://iam.example.com":                   false,
		"http://10.0.0.5.nip.io":                   false,
		"http://iam.example.com.svc.attacker.net":  false,
		"iamkit:8080":                              false,
		"":                                         false,
	} {
		if err := backChannelSafe(raw, false); (err == nil) != ok {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if backChannelSafe("http://iam.example.com", true) != nil {
		t.Error("ODYSSEY_ACCESS_ALLOW_HTTP lets a private network through")
	}
}
