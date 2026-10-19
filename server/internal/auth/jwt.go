// Package auth provides JWT authentication utilities and HTTP middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Sentinel errors for JWT verification failures.
var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

// JWTPayload is the JWT body — matches the TypeScript JwtPayload interface.
type JWTPayload struct {
	Sub   string `json:"sub"`           // GitHub numeric user ID
	Login string `json:"login"`         // GitHub username
	Exp   int64  `json:"exp"`           // Unix timestamp
	Iat   int64  `json:"iat,omitempty"` // Issued-at Unix timestamp
	Iss   string `json:"iss,omitempty"` // Issuer
	Aud   string `json:"aud,omitempty"` // Audience
}

// BypassUserID is the canonical Sub/Login for the implicit local admin used
// when DASHBOARD_AUTH=none (bypass mode). Handlers that fall back on a missing
// JWT payload MUST use this identity so per-user data (remotes, cost history,
// search scoping) is keyed consistently across restarts and matches /api/me.
const BypassUserID = "local"

// BypassPayload returns the canonical implicit-local-admin payload for bypass
// mode. A missing payload from PayloadFromContext can only occur in bypass mode
// — RequireAuth rejects unauthenticated requests with 401 before any handler
// runs when auth is enabled — so handlers may safely substitute this identity
// instead of returning 403/401.
func BypassPayload() JWTPayload {
	return JWTPayload{Sub: BypassUserID, Login: BypassUserID}
}

// PayloadOrBypass returns the request's JWT payload, or BypassPayload when there is none.
func PayloadOrBypass(ctx context.Context) JWTPayload {
	if p, ok := PayloadFromContext(ctx); ok {
		return p
	}
	return BypassPayload()
}

// jwtClaims embeds RegisteredClaims and carries our custom fields.
type jwtClaims struct {
	jwt.RegisteredClaims
	Login string `json:"login"`
}

// oauthStateClaims is the claims type for short-lived OAuth state tokens.
type oauthStateClaims struct {
	jwt.RegisteredClaims
}

// nonceClaims is the claims type for short-lived OAuth flow-binding nonce tokens.
type nonceClaims struct {
	jwt.RegisteredClaims
}

// OAuthStatePayload is the payload for short-lived OAuth state tokens.
// Kept separate from JWTPayload to prevent state tokens from being accepted
// where session tokens are expected.
type OAuthStatePayload struct {
	Sub string `json:"sub"` // always "oauth-state"
	Exp int64  `json:"exp"`
	Aud string `json:"aud"` // always "agent-dashboard:oauth-state"
}

const jwtIssuer = "agent-dashboard"
const jwtAudience = "agent-dashboard"

// SignJWT creates an HS256 JWT token. expiresInSeconds is added to now().
func SignJWT(payload JWTPayload, secret string, expiresInSeconds int64) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   payload.Sub,
			Issuer:    jwtIssuer,
			Audience:  jwt.ClaimStrings{jwtAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(expiresInSeconds) * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		Login: payload.Login,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signed, nil
}

// VerifyJWT validates an HS256 JWT and returns the payload.
// Returns ErrTokenInvalid for structural/signature errors.
// Returns ErrTokenExpired for tokens expired beyond the 60-second grace period.
// Within the grace period, the token is accepted as valid (jwt.WithLeeway(60s) applies to exp).
// Rejects tokens with sub == "oauth-state" as defense-in-depth against state token reuse.
func VerifyJWT(tokenStr, secret string) (JWTPayload, error) {
	var claims jwtClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	},
		jwt.WithIssuedAt(),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(jwtAudience),
		// WithLeeway applies to both iat and exp — 60 s post-expiry grace is acceptable for a local-only dashboard.
		jwt.WithLeeway(60*time.Second),
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return JWTPayload{}, ErrTokenExpired
		}
		return JWTPayload{}, ErrTokenInvalid
	}
	if !token.Valid {
		return JWTPayload{}, ErrTokenInvalid
	}

	// Defense-in-depth: reject OAuth state tokens used as session tokens.
	if claims.Subject == "oauth-state" {
		return JWTPayload{}, ErrTokenInvalid
	}

	exp := int64(0)
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Unix()
	}
	iat := int64(0)
	if claims.IssuedAt != nil {
		iat = claims.IssuedAt.Unix()
	}
	aud := ""
	if len(claims.Audience) > 0 {
		aud = claims.Audience[0]
	}

	return JWTPayload{
		Sub:   claims.Subject,
		Login: claims.Login,
		Exp:   exp,
		Iat:   iat,
		Iss:   claims.Issuer,
		Aud:   aud,
	}, nil
}

// SignOAuthState creates a short-lived HS256 JWT for use as the OAuth CSRF state parameter.
func SignOAuthState(secret string) (string, error) {
	claims := oauthStateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "oauth-state",
			Audience:  jwt.ClaimStrings{"agent-dashboard:oauth-state"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("sign oauth state: %w", err)
	}
	return signed, nil
}

// VerifyOAuthState validates an OAuth state token and returns its payload.
// Rejects tokens with wrong audience or subject.
func VerifyOAuthState(tokenStr, secret string) (OAuthStatePayload, error) {
	var claims oauthStateClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	},
		jwt.WithAudience("agent-dashboard:oauth-state"),
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return OAuthStatePayload{}, ErrTokenExpired
		}
		return OAuthStatePayload{}, ErrTokenInvalid
	}
	if !token.Valid {
		return OAuthStatePayload{}, ErrTokenInvalid
	}
	if claims.Subject != "oauth-state" {
		return OAuthStatePayload{}, ErrTokenInvalid
	}

	exp := int64(0)
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Unix()
	}
	aud := ""
	if len(claims.Audience) > 0 {
		aud = claims.Audience[0]
	}
	return OAuthStatePayload{
		Sub: claims.Subject,
		Exp: exp,
		Aud: aud,
	}, nil
}

const (
	nonceSubject  = "auth-nonce"
	nonceAudience = "agent-dashboard:auth-nonce"
	nonceTTL      = 10 * time.Minute
)

// GenerateNonce returns a signed short-lived JWT for OAuth flow binding.
// The nonce carries a random jti so each redirect produces a unique token.
// TTL is 10 minutes; callers must validate exp via ValidateNonce.
func GenerateNonce(secret string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate nonce: read random: %w", err)
	}
	jti := hex.EncodeToString(raw)

	claims := nonceClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   nonceSubject,
			Audience:  jwt.ClaimStrings{nonceAudience},
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(nonceTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("generate nonce: sign: %w", err)
	}
	return signed, nil
}

// ValidateNonce parses a nonce token and returns an error if the signature is
// invalid, the subject is wrong, or the token has expired.
func ValidateNonce(secret, tokenStr string) error {
	var claims nonceClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	},
		jwt.WithAudience(nonceAudience),
		// No leeway: nonces must be consumed promptly within their 10-minute window.
		jwt.WithLeeway(0),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return fmt.Errorf("nonce expired")
		}
		return fmt.Errorf("invalid nonce: %w", err)
	}
	if !token.Valid {
		return fmt.Errorf("invalid nonce token")
	}
	if claims.Subject != nonceSubject {
		return fmt.Errorf("invalid nonce subject")
	}
	return nil
}
