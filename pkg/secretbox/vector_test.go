package secretbox_test

import (
	"encoding/base64"

	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

// A fixed key and a value sealed to it by the console's implementation
// (frontend/src/lib/secretbox.ts; secretbox.test.ts holds the same vector).
var (
	vectorKey, _    = base64.StdEncoding.DecodeString("AAgPFh0kKzI5QEdOVVxjanF4f4aNlJuiqbC3vsXM01o=")
	vectorTarget    = secretbox.Target{Cluster: "cluster-1", Environment: "env-1", App: "portal-prod", Name: "API_KEY", Version: 7}
	vectorValue     = "contraseña ñandú 🔑"
	vectorSealed, _ = base64.StdEncoding.DecodeString("NyprRot9x2laBx+wbunZ1Nt/mr1+0X8c2JpfP25yihuZ6Og5k9YKPlO0q/mu+hVSWzxuoc0zPPWlGGIxKxw+Pnn1d7753d0p")
)
