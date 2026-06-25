package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// Principal is the resolved caller identity for an MCP request. It comes from the
// bearer token (per-user identity propagation, M17) or a default for local stdio.
type Principal struct {
	User   string
	Role   string
	Tenant string
	Scopes []string
}

func (p Principal) hasScope(s string) bool {
	for _, x := range p.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// Options configure the server's security.
type Options struct {
	Default Principal // used when there is no token (local stdio)
	RPS     float64   // per-principal rate limit (0 = off)
	Burst   int
}

func defaultOptions() *Options {
	return &Options{Default: Principal{User: "local", Role: "analyst", Scopes: []string{"metrics:read", "data:write"}}, RPS: 0}
}

// principalFromToken maps an SDK TokenInfo (validated by the verifier — audience,
// expiry) to a Principal. role/tenant ride in the token's Extra claims.
func principalFromToken(ti *auth.TokenInfo, def Principal) Principal {
	if ti == nil {
		return def
	}
	p := Principal{User: ti.UserID, Scopes: ti.Scopes}
	if r, ok := ti.Extra["role"].(string); ok {
		p.Role = r
	}
	if t, ok := ti.Extra["tenant"].(string); ok {
		p.Tenant = t
	}
	if p.Role == "" {
		p.Role = def.Role
	}
	return p
}

// --- per-principal rate limiter (lazy token bucket) ---

type rateLimiter struct {
	mu    sync.Mutex
	rps   float64
	burst float64
	b     map[string]*bucket
	now   func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if rps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = 1
	}
	return &rateLimiter{rps: rps, burst: float64(burst), b: map[string]*bucket{}, now: time.Now}
}

func (r *rateLimiter) allow(key string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	bk := r.b[key]
	if bk == nil {
		bk = &bucket{tokens: r.burst, last: now}
		r.b[key] = bk
	}
	bk.tokens += now.Sub(bk.last).Seconds() * r.rps
	if bk.tokens > r.burst {
		bk.tokens = r.burst
	}
	bk.last = now
	if bk.tokens >= 1 {
		bk.tokens--
		return true
	}
	return false
}

// DemoVerifier is a stand-in token verifier for development: it maps opaque
// tokens to identities and checks the token is intended for THIS server
// (audience) — the confused-deputy cure. Replace with a real OIDC/JWT verifier.
func DemoVerifier(audience string) auth.TokenVerifier {
	type id struct {
		role, tenant string
		scopes       []string
	}
	known := map[string]id{
		"analyst-token": {role: "analyst", tenant: "acme", scopes: []string{"metrics:read"}},
		"finance-token": {role: "finance", tenant: "acme", scopes: []string{"metrics:read"}},
		"admin-token":   {role: "admin", tenant: "acme", scopes: []string{"metrics:read", "data:write"}},
	}
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		i, ok := known[token]
		if !ok {
			return nil, fmt.Errorf("invalid token")
		}
		// A real verifier checks the token's `aud` claim == audience here.
		return &auth.TokenInfo{
			UserID:     i.role + "@" + i.tenant,
			Scopes:     i.scopes,
			Expiration: time.Now().Add(time.Hour),
			Extra:      map[string]any{"role": i.role, "tenant": i.tenant, "aud": audience},
		}, nil
	}
}
