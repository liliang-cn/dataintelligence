package runtime

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/liliang-cn/dataintelligence/governance"
)

// StaticUser is one bearer-token identity from auth.users.
//
// It exists for the deployment that needs two named people — one who proposes
// and one who adopts — before it has an identity provider. The token is
// compared in constant time and never logged; a real IdP belongs in auth.oidc.
type StaticUser struct {
	Name  string
	Role  string
	Token string
	Attrs map[string]string
}

// TokenCookie carries the same bearer token for the browser console, which
// cannot set an Authorization header on an EventSource or a plain link.
const TokenCookie = "di_token"

// UserCookie names the caller in open (dev) mode only, like X-DI-User.
const UserCookie = "di_user"

func requestToken(r *http.Request) string {
	if t := bearerToken(r); t != "" {
		return t
	}
	if c, err := r.Cookie(TokenCookie); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return ""
}

func (v *V1) staticPrincipal(r *http.Request) (governance.Principal, bool) {
	tok := requestToken(r)
	if tok == "" {
		return governance.Principal{}, false
	}
	for _, u := range v.Users {
		if u.Token != "" && subtle.ConstantTimeCompare([]byte(u.Token), []byte(tok)) == 1 {
			attrs := map[string]string{}
			for k, val := range u.Attrs {
				attrs[k] = val
			}
			return governance.Principal{User: u.Name, Role: orDefault(u.Role, "analyst"), Attrs: attrs, Engagement: v.Engagement}, true
		}
	}
	return governance.Principal{}, false
}

// devUser is the caller's name in open mode: X-DI-User, the console's
// di_user cookie, or "anon". Open mode is for development; it is how two
// people can be told apart on a laptop, not an authentication scheme.
func devUser(r *http.Request) string {
	if u := strings.TrimSpace(r.Header.Get("X-DI-User")); u != "" {
		return u
	}
	if c, err := r.Cookie(UserCookie); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	return "anon"
}

// Principal resolves the caller for other surfaces (the console) the same way
// /v1 does.
func (v *V1) Principal(r *http.Request) (governance.Principal, error) {
	p, ok, err := v.principalFrom(r)
	if !ok {
		if err == nil {
			err = errString("unauthenticated")
		}
		return governance.Principal{}, err
	}
	return p, nil
}

// AuthMode says how callers are identified: "users", "oidc", "users+oidc" or "open".
func (v *V1) AuthMode() string {
	switch {
	case len(v.Users) > 0 && v.Verify != nil:
		return "users+oidc"
	case len(v.Users) > 0:
		return "users"
	case v.Verify != nil:
		return "oidc"
	}
	return "open"
}
