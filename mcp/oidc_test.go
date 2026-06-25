package mcp

import (
	"context"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := GenerateKey(2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return priv
}

func mint(t *testing.T, priv *rsa.PrivateKey, kid string, c map[string]any) string {
	t.Helper()
	tok, err := SignJWT(priv, kid, c)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func baseClaims() map[string]any {
	return map[string]any{
		"iss": "https://idp.local/di", "sub": "alice", "aud": "dataintelligence",
		"role": "finance", "tenant": "acme", "scope": "metrics:read",
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	}
}

func staticVerifier(t *testing.T, priv *rsa.PrivateKey) *OIDC {
	t.Helper()
	pub, err := MarshalPublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewOIDC(OIDCConfig{Issuer: "https://idp.local/di", Audience: "dataintelligence", PublicKeyPEM: pub, KeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOIDCValidToken(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)
	ti, err := o.verify(context.Background(), mint(t, priv, "k1", baseClaims()), nil)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if ti.UserID != "alice" || ti.Extra["role"] != "finance" || ti.Extra["tenant"] != "acme" {
		t.Fatalf("claims not mapped: %+v", ti)
	}
	if len(ti.Scopes) != 1 || ti.Scopes[0] != "metrics:read" {
		t.Fatalf("scopes not mapped: %v", ti.Scopes)
	}
}

func TestOIDCWrongAudienceRejected(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)
	c := baseClaims()
	c["aud"] = "some-other-service" // confused-deputy attempt
	if _, err := o.verify(context.Background(), mint(t, priv, "k1", c), nil); err == nil {
		t.Fatal("token for another audience must be rejected")
	}
}

func TestOIDCExpiredRejected(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)
	c := baseClaims()
	c["exp"] = time.Now().Add(-2 * time.Hour).Unix()
	if _, err := o.verify(context.Background(), mint(t, priv, "k1", c), nil); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestOIDCTamperedRejected(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)
	tok := mint(t, priv, "k1", baseClaims())
	tampered := tok[:len(tok)-2] + "AB" // mangle the signature tail
	if _, err := o.verify(context.Background(), tampered, nil); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestOIDCWrongIssuerRejected(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)
	c := baseClaims()
	c["iss"] = "https://evil.example/"
	if _, err := o.verify(context.Background(), mint(t, priv, "k1", c), nil); err == nil {
		t.Fatal("untrusted issuer must be rejected")
	}
}

func TestOIDCViaJWKS(t *testing.T) {
	priv := genKey(t)
	jwks, err := JWKSJSON("k1", &priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer srv.Close()

	o, err := NewOIDC(OIDCConfig{Issuer: "https://idp.local/di", Audience: "dataintelligence", JWKSURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.verify(context.Background(), mint(t, priv, "k1", baseClaims()), nil); err != nil {
		t.Fatalf("JWKS-verified token rejected: %v", err)
	}
	// A token signed by an unknown key (kid not in JWKS) must fail.
	other := genKey(t)
	if _, err := o.verify(context.Background(), mint(t, other, "k1", baseClaims()), nil); err == nil {
		t.Fatal("token signed by a key not in the JWKS must be rejected")
	}
}
