package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGeneratedEdgeAuthVerifiesIDToken(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the generated Lambda@Edge module")
	}

	dir := t.TempDir()
	if _, err := Run(dir, Params{
		Project: "docs", Region: "us-east-1", Driver: "static", Framework: "astro", Auth: true,
	}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}

	moduleDir := filepath.Join(dir, "node_modules", "@aws-sdk", "client-ssm")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "package.json"), `{"type":"module","exports":"./index.js"}`)
	writeTestFile(t, filepath.Join(moduleDir, "index.js"), `
export class GetParametersByPathCommand {
  constructor(input) { this.input = input }
}
export class SSMClient {
  async send(command) {
    const prefix = command.input.Path
    return { Parameters: Object.entries(globalThis.__edgeAuthConfig).map(([name, Value]) => ({
      Name: prefix + name, Value,
    })) }
  }
}
`)
	writeTestFile(t, filepath.Join(dir, "oidc-test.mjs"), edgeAuthOIDCTest)

	cmd := exec.Command("node", "oidc-test.mjs")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated edge auth test failed: %v\n%s", err, out)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

const edgeAuthOIDCTest = `
import { generateKeyPairSync, sign } from 'node:crypto'

const issuer = 'https://issuer.example.test'
const clientID = 'client-123'
const domain = 'preview.example.com'
const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
const { privateKey: forgedKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
const jwk = publicKey.export({ format: 'jwk' })
Object.assign(jwk, { kid: 'current-key', use: 'sig', alg: 'RS256', key_ops: ['verify'] })

globalThis.__edgeAuthConfig = {
  issuer,
  client_id: clientID,
  client_secret: 'client-secret',
  session_secret: 'session-secret-with-enough-entropy',
  allowed_domain: 'example.com',
  mode: 'per-app',
  routing: 'directory',
}

let scenario = 'valid'
let expectedNonce = ''
globalThis.fetch = async (url) => {
  url = String(url)
  if (url === issuer + '/.well-known/openid-configuration') {
    return json({
      issuer,
      authorization_endpoint: issuer + '/authorize',
      token_endpoint: issuer + '/token',
      jwks_uri: issuer + '/jwks',
      id_token_signing_alg_values_supported: ['RS256'],
    })
  }
  if (url === issuer + '/jwks') return json({ keys: [jwk] })
  if (url === issuer + '/token') return json({ id_token: token(scenario, expectedNonce) })
  throw new Error('unexpected fetch: ' + url)
}

const { handler } = await import('./deploy/edge-auth/index.mjs')
const login = await handler(event('pr-42.' + domain, '/docs', 'page=1'))
equal(login.status, '302', 'login redirects')
const location = new URL(login.headers.location[0].value)
const state = location.searchParams.get('state')
expectedNonce = location.searchParams.get('nonce')
ok(state, 'authorization request has state')
ok(expectedNonce, 'authorization request has nonce')
const bindingCookie = login.headers['set-cookie'][0].value.split(';')[0]
ok(bindingCookie.startsWith('kagerou_oidc_state='), 'login binds nonce to browser cookie')

const valid = await callback('valid', bindingCookie)
equal(valid.status, '302', 'valid signed token is accepted')
equal(valid.headers['set-cookie'].length, 2, 'success sets session and clears state cookie')

for (const rejected of ['forged', 'wrong-aud', 'wrong-iss', 'expired', 'nonce-mismatch', 'unverified-email', 'wrong-domain']) {
  const response = await callback(rejected, bindingCookie)
  equal(response.status, '403', rejected + ' token is rejected')
}

const unbound = await callback('valid', 'kagerou_oidc_state=not-signed')
equal(unbound.status, '403', 'callback without matching browser binding is rejected')

async function callback(next, cookie) {
  scenario = next
  const query = new URLSearchParams({ code: 'authorization-code', state }).toString()
  return handler(event('auth.' + domain, '/_kagerou/auth/callback', query, cookie))
}

function token(kind, nonce) {
  const now = Math.floor(Date.now() / 1000)
  const claims = {
    iss: issuer,
    aud: clientID,
    exp: now + 300,
    iat: now,
    nonce,
    sub: 'user-1',
    email: 'user@example.com',
    email_verified: true,
    hd: 'example.com',
  }
  if (kind === 'wrong-aud') claims.aud = 'another-client'
  if (kind === 'wrong-iss') claims.iss = 'https://attacker.example.test'
  if (kind === 'expired') claims.exp = now - 300
  if (kind === 'nonce-mismatch') claims.nonce = 'attacker-nonce'
  if (kind === 'unverified-email') claims.email_verified = false
  if (kind === 'wrong-domain') claims.hd = 'attacker.example'
  return jwt(claims, kind === 'forged' ? forgedKey : privateKey)
}

function jwt(claims, key) {
  const header = Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'current-key', typ: 'JWT' })).toString('base64url')
  const payload = Buffer.from(JSON.stringify(claims)).toString('base64url')
  const input = header + '.' + payload
  return input + '.' + sign('RSA-SHA256', Buffer.from(input), key).toString('base64url')
}

function event(host, uri, querystring = '', cookie = '') {
  const headers = { host: [{ key: 'Host', value: host }] }
  if (cookie) headers.cookie = [{ key: 'Cookie', value: cookie }]
  return { Records: [{ cf: { request: { headers, uri, querystring } } }] }
}

function json(value) {
  return { ok: true, status: 200, json: async () => value }
}

function equal(got, want, message) {
  if (got !== want) throw new Error(message + ': got ' + JSON.stringify(got) + ', want ' + JSON.stringify(want))
}

function ok(value, message) {
  if (!value) throw new Error(message)
}
`
