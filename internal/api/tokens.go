// Package api is the Router Core API — the only surface the web UI talks to
// (DESIGN §3). It is code-first with Huma (D-8): handlers are typed funcs over
// net/http and the OpenAPI 3.1 spec is generated from them. The live spec/docs
// endpoints are dev-only (D-9).
package api

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenTTL is how long an issued JWT stays valid.
const tokenTTL = 12 * time.Hour

// tokenIssuer signs and verifies the agent's JWTs. The signing key is derived
// from the stored password hash, so changing the admin password invalidates all
// outstanding tokens (a useful side effect, no separate key management needed).
type tokenIssuer struct {
	key []byte
}

func newTokenIssuer(passwordHash string) *tokenIssuer {
	// The bcrypt hash is already high-entropy and secret; use it as the HMAC key.
	return &tokenIssuer{key: []byte("veilbridge-v1:" + passwordHash)}
}

// issue returns a signed token and its expiry.
func (t *tokenIssuer) issue() (string, time.Time, error) {
	if len(t.key) == 0 {
		return "", time.Time{}, fmt.Errorf("api: no signing key (password not set)")
	}
	exp := timeNow().Add(tokenTTL)
	claims := jwt.RegisteredClaims{
		Issuer:    "veilbridge",
		Subject:   "admin",
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(timeNow()),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(t.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("api: sign token: %w", err)
	}
	return signed, exp, nil
}

// verify reports whether raw is a valid, unexpired token signed with our key.
func (t *tokenIssuer) verify(raw string) bool {
	if len(t.key) == 0 || raw == "" {
		return false
	}
	_, err := jwt.Parse(raw, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		return t.key, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	return err == nil
}

// timeNow is a seam for tests.
var timeNow = time.Now
