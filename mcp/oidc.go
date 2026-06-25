package mcp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// OIDCConfig configures a real JWT/OIDC token verifier. Provide either a JWKSURL
// (a real IdP's key set) or a static PublicKeyPEM (local/dev). Issuer and
// Audience are validated on every token — Audience is the confused-deputy cure:
// a token minted for another service is rejected here.
type OIDCConfig struct {
	Issuer       string
	Audience     string
	JWKSURL      string // http(s) URL of the IdP JWKS (preferred for real IdPs)
	PublicKeyPEM []byte // OR a static RSA public key (SPKI PEM)
	KeyID        string // kid for the static key (optional)
	Leeway       time.Duration
}

// OIDC is a stdlib RS256 verifier: signature (via JWKS or a static key), then
// issuer / audience / expiry / not-before, then claims → identity.
type OIDC struct {
	cfg    OIDCConfig
	jwks   *jwksCache
	static map[string]*rsa.PublicKey
	leeway time.Duration
}

// NewOIDC builds a verifier from config.
func NewOIDC(cfg OIDCConfig) (*OIDC, error) {
	if cfg.Audience == "" {
		return nil, errors.New("oidc: Audience is required (confused-deputy guard)")
	}
	o := &OIDC{cfg: cfg, leeway: cfg.Leeway, static: map[string]*rsa.PublicKey{}}
	if o.leeway == 0 {
		o.leeway = 60 * time.Second
	}
	switch {
	case cfg.JWKSURL != "":
		o.jwks = &jwksCache{url: cfg.JWKSURL, ttl: 10 * time.Minute, now: time.Now, client: &http.Client{Timeout: 10 * time.Second}}
	case len(cfg.PublicKeyPEM) > 0:
		pub, err := parseRSAPublicKeyPEM(cfg.PublicKeyPEM)
		if err != nil {
			return nil, err
		}
		o.static[cfg.KeyID] = pub
	default:
		return nil, errors.New("oidc: set JWKSURL or PublicKeyPEM")
	}
	return o, nil
}

// Verifier adapts to the SDK's auth.TokenVerifier.
func (o *OIDC) Verifier() auth.TokenVerifier { return o.verify }

// invalid wraps auth.ErrInvalidToken so RequireBearerToken answers 401 (not 500)
// for any token-level rejection — bad signature, wrong audience, expired, etc.
func invalid(format string, a ...any) error {
	return fmt.Errorf("oidc: "+format+": %w", append(a, auth.ErrInvalidToken)...)
}

func (o *OIDC) verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, invalid("malformed JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := jsonB64(parts[0], &hdr); err != nil {
		return nil, invalid("bad header: %v", err)
	}
	if hdr.Alg != "RS256" {
		return nil, invalid("unsupported alg %q (want RS256)", hdr.Alg)
	}

	key, err := o.keyFor(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, invalid("bad signature encoding: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, invalid("signature verification failed")
	}

	var c claims
	if err := jsonB64(parts[1], &c); err != nil {
		return nil, invalid("bad claims: %v", err)
	}
	now := time.Now()
	if o.cfg.Issuer != "" && c.Iss != o.cfg.Issuer {
		return nil, invalid("issuer %q not trusted", c.Iss)
	}
	if !c.Aud.contains(o.cfg.Audience) {
		return nil, invalid("token audience %v is not %q (confused-deputy)", c.Aud, o.cfg.Audience)
	}
	if c.Exp != 0 && now.After(time.Unix(c.Exp, 0).Add(o.leeway)) {
		return nil, invalid("token expired")
	}
	if c.Nbf != 0 && now.Add(o.leeway).Before(time.Unix(c.Nbf, 0)) {
		return nil, invalid("token not yet valid")
	}

	ti := &auth.TokenInfo{
		UserID:     c.Sub,
		Scopes:     c.scopes(),
		Expiration: time.Unix(c.Exp, 0),
		Extra:      map[string]any{"role": c.Role, "tenant": c.Tenant, "region": c.Region, "aud": o.cfg.Audience, "iss": c.Iss},
	}
	return ti, nil
}

func (o *OIDC) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if o.jwks != nil {
		return o.jwks.key(ctx, kid)
	}
	if k, ok := o.static[kid]; ok {
		return k, nil
	}
	// A single static key with no kid match: accept if there's exactly one.
	if len(o.static) == 1 {
		for _, k := range o.static {
			return k, nil
		}
	}
	return nil, invalid("no key for kid %q", kid)
}

// --- claims ---

type claims struct {
	Iss    string   `json:"iss"`
	Sub    string   `json:"sub"`
	Aud    audience `json:"aud"`
	Exp    int64    `json:"exp"`
	Nbf    int64    `json:"nbf"`
	Scope  string   `json:"scope"` // space-delimited (OAuth2)
	Scp    []string `json:"scp"`   // array form (some IdPs)
	Role   string   `json:"role"`
	Tenant string   `json:"tenant"`
	Region string   `json:"region"`
}

func (c claims) scopes() []string {
	if len(c.Scp) > 0 {
		return c.Scp
	}
	if c.Scope == "" {
		return nil
	}
	return strings.Fields(c.Scope)
}

// audience accepts a JWT "aud" that is either a string or an array of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = audience{s}
		return nil
	}
	var ss []string
	if err := json.Unmarshal(b, &ss); err != nil {
		return err
	}
	*a = ss
	return nil
}

func (a audience) contains(s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

// --- JWKS cache ---

type jwksCache struct {
	url    string
	ttl    time.Duration
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (j *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	stale := j.keys == nil || j.now().Sub(j.fetched) > j.ttl
	_, have := j.keys[kid]
	j.mu.Unlock()
	if stale || !have {
		if err := j.refresh(ctx); err != nil && !have {
			return nil, err
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, invalid("kid %q not in JWKS", kid)
}

func (j *jwksCache) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var doc jwksDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}
	keys, err := doc.toKeys()
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.keys, j.fetched = keys, j.now()
	j.mu.Unlock()
	return nil
}

type jwksDoc struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (d jwksDoc) toKeys() (map[string]*rsa.PublicKey, error) {
	out := map[string]*rsa.PublicKey{}
	for _, k := range d.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err := b64.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		eb, err := b64.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("oidc: JWKS has no RSA keys")
	}
	return out, nil
}

// --- dev helpers: mint tokens + publish a JWKS (for local testing only) ---

// GenerateKey makes an RSA signing key for the dev issuer (`di token gen-key`).
func GenerateKey(bits int) (*rsa.PrivateKey, error) {
	if bits == 0 {
		bits = 2048
	}
	return rsa.GenerateKey(rand.Reader, bits)
}

// SignJWT mints an RS256 JWT. Used by `di token` to issue dev tokens; a real IdP
// owns this in production.
func SignJWT(priv *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	h, err := json.Marshal(hdr)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64.EncodeToString(sig), nil
}

// JWKSJSON renders a one-key JWKS document for a public key.
func JWKSJSON(kid string, pub *rsa.PublicKey) ([]byte, error) {
	doc := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64.EncodeToString(pub.N.Bytes()),
		"e": b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	return json.MarshalIndent(doc, "", "  ")
}

// MarshalPrivateKeyPEM / MarshalPublicKeyPEM encode keys for `di token gen-key`.
func MarshalPrivateKeyPEM(priv *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509MarshalPKCS8(priv)})
}

func MarshalPublicKeyPEM(pub *rsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func x509MarshalPKCS8(priv *rsa.PrivateKey) []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	return der
}

// ParsePrivateKeyPEM loads a PKCS8/PKCS1 RSA private key (for `di token mint`).
func ParsePrivateKeyPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("oidc: no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("oidc: not an RSA key")
	}
	return rk, nil
}

func parseRSAPublicKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("oidc: no PEM block in public key")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("oidc: not an RSA public key")
	}
	return pub, nil
}

// --- shared base64url + json helpers ---

var b64 = base64.RawURLEncoding

func jsonB64(seg string, v any) error {
	raw, err := b64.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
