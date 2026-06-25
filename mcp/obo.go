package mcp

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// WarehouseIdentity is the end-user identity propagated to the warehouse after an
// on-behalf-of exchange. It binds to the DB session (app.* GUCs) so the engine's
// own RLS enforces on the real user.
type WarehouseIdentity struct {
	User   string
	Role   string
	Tenant string
	Region string
}

// ExchangeToken implements an RFC 8693 token exchange. The MCP server holds a
// validated *subject* token (the caller) and mints a fresh, short-lived token
// scoped to the warehouse *audience* — it never forwards the client's token
// downstream (that would be the confused deputy). The minted token carries the
// propagated identity and an `act` (actor) claim naming this server as the
// delegate, per the spec. Returns the token and the identity to bind to the DB.
//
// In production the authorization server performs this exchange; here the MCP
// server signs with the dev key so the full chain is demonstrable end to end.
func ExchangeToken(priv *rsa.PrivateKey, kid, actor, warehouseAud string, caller *auth.TokenInfo, ttl time.Duration) (string, WarehouseIdentity, error) {
	if caller == nil {
		return "", WarehouseIdentity{}, fmt.Errorf("oidc: no subject token to exchange")
	}
	id := WarehouseIdentity{
		User:   caller.UserID,
		Role:   claimString(caller.Extra, "role"),
		Tenant: claimString(caller.Extra, "tenant"),
		Region: claimString(caller.Extra, "region"),
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now()
	claims := map[string]any{
		"iss":  actor, // the MCP server acts as the (delegated) issuer
		"sub":  id.User,
		"aud":  warehouseAud,                 // re-scoped: this token is only valid AT the warehouse
		"act":  map[string]any{"sub": actor}, // RFC 8693 actor claim
		"role": id.Role, "tenant": id.Tenant, "region": id.Region,
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(ttl).Unix(),
	}
	tok, err := SignJWT(priv, kid, claims)
	if err != nil {
		return "", WarehouseIdentity{}, err
	}
	return tok, id, nil
}

func claimString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
