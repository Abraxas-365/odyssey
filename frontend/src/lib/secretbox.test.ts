// @vitest-environment node
// (WebCrypto rejects jsdom's ArrayBuffers; this module touches no DOM.)
import { describe, expect, it } from "vitest"
import { Aes256Gcm, CipherSuite, DhkemX25519HkdfSha256, HkdfSha256 } from "@hpke/core"

import { fromBase64, MAX_SECRET_VALUE, sealSecret, secretInfo, type SecretTarget } from "./secretbox"

// The same fixed key and vector as pkg/secretbox/vector_test.go: Go opens
// what this module sealed, so the browser and the agent cannot drift apart.
const PRIVATE = "AAgPFh0kKzI5QEdOVVxjanF4f4aNlJuiqbC3vsXM01o="
const PUBLIC = "yP7Kgb4ZbN8sreq/E8SQPXYy3OSVWqaLbl2a3vVOJhY="
const TARGET: SecretTarget = { cluster: "cluster-1", environment: "env-1", app: "portal-prod", name: "API_KEY", version: 7 }
const VALUE = "contraseña ñandú 🔑"
const SEALED = "NyprRot9x2laBx+wbunZ1Nt/mr1+0X8c2JpfP25yihuZ6Og5k9YKPlO0q/mu+hVSWzxuoc0zPPWlGGIxKxw+Pnn1d7753d0p"

const suite = new CipherSuite({ kem: new DhkemX25519HkdfSha256(), kdf: new HkdfSha256(), aead: new Aes256Gcm() })

async function open(sealedB64: string, t: SecretTarget): Promise<string> {
  const sealed = fromBase64(sealedB64)
  const recipientKey = await suite.kem.deserializePrivateKey(fromBase64(PRIVATE).slice().buffer)
  const pt = await suite.open(
    { recipientKey, enc: sealed.slice(0, 32).buffer, info: secretInfo(t).slice().buffer },
    sealed.slice(32).buffer,
  )
  return new TextDecoder().decode(pt)
}

describe("secretbox", () => {
  it("seals so the cluster key opens it for that target only", async () => {
    const sealed = await sealSecret(fromBase64(PUBLIC), TARGET, VALUE)
    expect(await open(sealed, TARGET)).toBe(VALUE)
    await expect(open(sealed, { ...TARGET, version: 8 })).rejects.toThrow()
    await expect(open(sealed, { ...TARGET, name: "OTHER" })).rejects.toThrow()
    await expect(open(sealed, { ...TARGET, app: "other-prod" })).rejects.toThrow()
  })

  it("opens the vector Go also opens", async () => {
    expect(await open(SEALED, TARGET)).toBe(VALUE)
  })

  it("refuses values over the limit", async () => {
    await expect(sealSecret(fromBase64(PUBLIC), TARGET, "x".repeat(MAX_SECRET_VALUE + 1))).rejects.toThrow(/demasiado largo/)
  })

  it("binds the info exactly as pkg/secretbox", () => {
    expect(new TextDecoder().decode(secretInfo(TARGET))).toBe("odicea/secret/v1\0cluster-1\0env-1\0portal-prod\0API_KEY\x007")
  })
})
