package mcp

import (
	"context"
	"testing"
	"time"
)

func TestExchangeTokenRescopesToWarehouse(t *testing.T) {
	priv := genKey(t)
	o := staticVerifier(t, priv)

	// A caller token (audience = the MCP server) carrying a manager identity.
	c := baseClaims()
	c["role"] = "manager"
	c["region"] = "South"
	caller, err := o.verify(context.Background(), mint(t, priv, "k1", c), nil)
	if err != nil {
		t.Fatalf("verify caller: %v", err)
	}

	// Exchange it for a warehouse-audience token (RFC 8693).
	whTok, id, err := ExchangeToken(priv, "k1", "di-mcp", "meridian-warehouse", caller, time.Minute)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.User != "alice" || id.Role != "manager" || id.Region != "South" {
		t.Fatalf("identity not propagated: %+v", id)
	}

	// The exchanged token must be re-scoped to the warehouse — NOT the MCP server.
	pub, err := MarshalPublicKeyPEM(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	whVerifier, err := NewOIDC(OIDCConfig{Issuer: "di-mcp", Audience: "meridian-warehouse", PublicKeyPEM: pub, KeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	ti, err := whVerifier.verify(context.Background(), whTok, nil)
	if err != nil {
		t.Fatalf("warehouse token rejected: %v", err)
	}
	if ti.UserID != "alice" || ti.Extra["region"] != "South" {
		t.Fatalf("warehouse token claims wrong: %+v", ti)
	}

	// And it must NOT be accepted at the original MCP-server audience (re-scoped).
	if _, err := o.verify(context.Background(), whTok, nil); err == nil {
		t.Fatal("exchanged warehouse token must not be valid at the MCP-server audience")
	}
}
