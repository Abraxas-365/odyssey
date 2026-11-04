// secretbox seals a managed secret's value to its cluster in the browser, so
// the control plane only ever carries bytes it cannot read. It mirrors
// pkg/secretbox (Go) exactly: same suite, same info; the test vector in
// secretbox.test.ts is opened by pkg/secretbox's TestVector.
import { Aes256Gcm, CipherSuite, DhkemX25519HkdfSha256, HkdfSha256 } from "@hpke/core"

/** The only suite (pkg/secretbox.Suite). */
export const SUITE = "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/AES-256-GCM"

/** pkg/secretbox.MaxValue, in bytes of UTF-8. */
export const MAX_SECRET_VALUE = 64 * 1024

export type SecretTarget = {
  cluster: string
  environment: string
  app: string
  name: string
  version: number
}

const suite = new CipherSuite({ kem: new DhkemX25519HkdfSha256(), kdf: new HkdfSha256(), aead: new Aes256Gcm() })

/** The HPKE info string: "odicea/secret/v1" NUL cluster NUL environment NUL app NUL name NUL version. */
export function secretInfo(t: SecretTarget): Uint8Array {
  return new TextEncoder().encode(["odicea/secret/v1", t.cluster, t.environment, t.app, t.name, String(t.version)].join("\0"))
}

/** sealSecret returns enc || ciphertext, base64 (std, padded) for the API. */
export async function sealSecret(publicKey: Uint8Array, t: SecretTarget, value: string): Promise<string> {
  const plain = new TextEncoder().encode(value)
  if (plain.byteLength > MAX_SECRET_VALUE) throw new Error("El valor es demasiado largo (máximo 64 KB).")
  const recipientPublicKey = await suite.kem.deserializePublicKey(publicKey.slice().buffer)
  const { enc, ct } = await suite.seal({ recipientPublicKey, info: secretInfo(t).slice().buffer }, plain.slice().buffer)
  const out = new Uint8Array(enc.byteLength + ct.byteLength)
  out.set(new Uint8Array(enc), 0)
  out.set(new Uint8Array(ct), enc.byteLength)
  return toBase64(out)
}

export function fromBase64(s: string): Uint8Array {
  const bin = atob(s)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export function toBase64(b: Uint8Array): string {
  let bin = ""
  for (const x of b) bin += String.fromCharCode(x)
  return btoa(bin)
}
