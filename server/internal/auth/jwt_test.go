package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/stretchr/testify/require"
)

// computeTestHMAC creates a valid HS256 signature for crafting tampered test tokens.
// golang-jwt uses the same HMAC-SHA256 signing format (HMAC over "header.payload").
func computeTestHMAC(data, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func TestSignAndVerifyJWT(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	payload := auth.JWTPayload{Sub: "12345", Login: "testuser"}

	token, err := auth.SignJWT(payload, secret, 3600)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	got, err := auth.VerifyJWT(token, secret)
	require.NoError(t, err)
	require.Equal(t, "12345", got.Sub)
	require.Equal(t, "testuser", got.Login)
}

func TestVerifyJWT_Expired(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	payload := auth.JWTPayload{Sub: "12345", Login: "testuser"}

	// Use a TTL well outside the 60-second leeway so the expiry is unambiguous.
	token, err := auth.SignJWT(payload, secret, -120) // expired 2 minutes ago
	require.NoError(t, err)

	_, err = auth.VerifyJWT(token, secret)
	require.ErrorIs(t, err, auth.ErrTokenExpired)
}

func TestVerifyJWT_WrongSecret(t *testing.T) {
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "x", Login: "x"}, "secret1", 3600)
	require.NoError(t, err)

	_, err = auth.VerifyJWT(token, "secret2")
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestVerifyJWT_MalformedToken(t *testing.T) {
	_, err := auth.VerifyJWT("not.a.valid.jwt.token.at.all", "secret")
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestVerifyJWT_InvalidHeader(t *testing.T) {
	// Header: {"alg":"RS256","typ":"JWT"} — valid structure but wrong alg
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","exp":9999999999}`))
	token := header + "." + body + ".fakesig"
	_, err := auth.VerifyJWT(token, "secret")
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestVerifyJWT_WrongIssuer(t *testing.T) {
	// Construct a structurally valid token signed with the correct secret but wrong iss.
	secret := "test-secret-32chars-long-minimum!"
	payload := auth.JWTPayload{Sub: "12345", Login: "testuser", Iss: "other-service", Aud: "agent-dashboard"}
	token, err := auth.SignJWT(payload, secret, 3600)
	require.NoError(t, err)
	// SignJWT overwrites Iss, so craft the raw token manually to inject a wrong issuer.
	_ = token
	// Use a manually assembled payload to bypass SignJWT's normalization.
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	bod := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"12345","login":"testuser","isAdmin":false,"exp":9999999999,"iss":"evil","aud":"agent-dashboard"}`))
	mac := computeTestHMAC(hdr+"."+bod, secret)
	tampered := hdr + "." + bod + "." + base64.RawURLEncoding.EncodeToString(mac)
	_, err = auth.VerifyJWT(tampered, secret)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestVerifyJWT_WrongAudience(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	bod := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"12345","login":"testuser","isAdmin":false,"exp":9999999999,"iss":"agent-dashboard","aud":"other-audience"}`))
	mac := computeTestHMAC(hdr+"."+bod, secret)
	tampered := hdr + "." + bod + "." + base64.RawURLEncoding.EncodeToString(mac)
	_, err := auth.VerifyJWT(tampered, secret)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

// TestVerifyJWT_LeewayAcceptsRecentlyExpired documents the 60-second grace period:
// a token expired 30 seconds ago must be accepted as valid.
func TestVerifyJWT_LeewayAcceptsRecentlyExpired(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	payload := auth.JWTPayload{Sub: "12345", Login: "testuser"}

	// Expired 30 seconds ago — within the 60-second leeway window.
	token, err := auth.SignJWT(payload, secret, -30)
	require.NoError(t, err)

	_, err = auth.VerifyJWT(token, secret)
	require.NoError(t, err, "token expired 30s ago should be accepted within the 60s leeway")
}

// TestVerifyJWT_OAuthStateTokenRejected documents that a valid OAuth state token
// (sub == "oauth-state") is rejected by VerifyJWT as a defense-in-depth measure
// against cross-type token reuse.
func TestVerifyJWT_OAuthStateTokenRejected(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	stateToken, err := auth.SignOAuthState(secret)
	require.NoError(t, err)
	_, err = auth.VerifyJWT(stateToken, secret)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

// TestVerifyOAuthState_SessionTokenRejected documents that a regular session token
// is rejected by VerifyOAuthState — its audience does not match the oauth-state contract.
func TestVerifyOAuthState_SessionTokenRejected(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	claims := auth.JWTPayload{Sub: "user-123", Login: "testuser"}
	sessionToken, err := auth.SignJWT(claims, secret, 3600)
	require.NoError(t, err)
	_, err = auth.VerifyOAuthState(sessionToken, secret)
	require.ErrorIs(t, err, auth.ErrTokenInvalid)
}

func TestGenerateAndValidateNonce(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	nonce, err := auth.GenerateNonce(secret)
	require.NoError(t, err)
	require.NotEmpty(t, nonce)

	err = auth.ValidateNonce(secret, nonce)
	require.NoError(t, err)
}

func TestValidateNonce_WrongSecret(t *testing.T) {
	nonce, err := auth.GenerateNonce("secret-a-32chars-long-minimum!!")
	require.NoError(t, err)

	err = auth.ValidateNonce("secret-b-32chars-long-minimum!!", nonce)
	require.Error(t, err)
}

func TestValidateNonce_MalformedToken(t *testing.T) {
	err := auth.ValidateNonce("any-secret", "not.a.valid.jwt")
	require.Error(t, err)
}

func TestValidateNonce_EmptyToken(t *testing.T) {
	err := auth.ValidateNonce("any-secret", "")
	require.Error(t, err)
}

// TestValidateNonce_SessionTokenRejected ensures a valid session token is not accepted as a nonce.
func TestValidateNonce_SessionTokenRejected(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	sessionToken, err := auth.SignJWT(auth.JWTPayload{Sub: "12345", Login: "alice"}, secret, 3600)
	require.NoError(t, err)

	err = auth.ValidateNonce(secret, sessionToken)
	require.Error(t, err, "session token must not be accepted as a nonce")
}

// TestGenerateNonce_Uniqueness verifies that two generated nonces differ (random jti).
func TestGenerateNonce_Uniqueness(t *testing.T) {
	secret := "test-secret-32chars-long-minimum!"
	n1, err := auth.GenerateNonce(secret)
	require.NoError(t, err)
	n2, err := auth.GenerateNonce(secret)
	require.NoError(t, err)
	require.NotEqual(t, n1, n2)
}
