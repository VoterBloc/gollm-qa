package server

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwksFixture spins up a tiny httptest.Server that serves a JWKS document
// for a single RSA public key, plus helpers to mint signed tokens for
// tests. Issuer points at the test server's URL.
type jwksFixture struct {
	server *httptest.Server
	priv   *rsa.PrivateKey
	keyID  string
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyID := "test-kid-mothman"

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksResponse(keyID, &priv.PublicKey))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &jwksFixture{server: srv, priv: priv, keyID: keyID}
}

func (f *jwksFixture) issuer() string { return f.server.URL }

// signToken mints a token with overridable issuer / subject / expiry. Use
// the zero time for expiresAt to mint an already-expired token.
func (f *jwksFixture) signToken(t *testing.T, issuer, sub string, expiresAt time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": issuer,
		"sub": sub,
		"iat": time.Now().Add(-1 * time.Minute).Unix(),
		"nbf": time.Now().Add(-1 * time.Minute).Unix(),
	}
	if !expiresAt.IsZero() {
		claims["exp"] = expiresAt.Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.keyID
	signed, err := tok.SignedString(f.priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// jwksResponse serializes a single RSA public key into a JWKS document.
func jwksResponse(kid string, pub *rsa.PublicKey) any {
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}
}

func TestClerkAuth_DevModeWhenIssuerEmpty(t *testing.T) {
	srv, err := New(Config{ClerkIssuer: ""}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := do(t, srv, http.MethodGet, "/v1/configs")
	if w.Code != http.StatusOK {
		t.Errorf("dev mode: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_RejectsMissingBearer(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := do(t, srv, http.MethodGet, "/v1/configs")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing bearer") {
		t.Errorf("body should mention missing bearer, got %s", w.Body.String())
	}
}

func TestClerkAuth_RejectsExpiredToken(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok := f.signToken(t, f.issuer(), "user_chupacabra", time.Now().Add(-1*time.Hour))
	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", tok)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired token: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_RejectsWrongIssuer(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok := f.signToken(t, "https://imposter.cryptid.example", "user_jersey_devil", time.Now().Add(time.Hour))
	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", tok)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong issuer: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_RejectsBadSignature(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok := f.signToken(t, f.issuer(), "user_yeti", time.Now().Add(time.Hour))
	// Tamper with the signature segment by flipping a character.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %d segments", len(parts))
	}
	parts[2] = parts[2][:len(parts[2])-3] + "AAA"
	tampered := strings.Join(parts, ".")

	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", tampered)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_RejectsMalformedToken(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", "definitely.not.a.real.jwt")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("malformed token: want 401, got %d", w.Code)
	}
}

func TestClerkAuth_AcceptsValidToken(t *testing.T) {
	f := newJWKSFixture(t)
	configsDir := t.TempDir()
	srv, err := New(Config{ClerkIssuer: f.issuer(), ConfigsDir: configsDir}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok := f.signToken(t, f.issuer(), "user_2abc_loch_ness", time.Now().Add(time.Hour))
	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", tok)
	if w.Code != http.StatusOK {
		t.Errorf("valid token: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_HealthAndOpenAPIStayPublic(t *testing.T) {
	f := newJWKSFixture(t)
	srv, err := New(Config{ClerkIssuer: f.issuer()}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, path := range []string{"/health", "/openapi.json"} {
		w := do(t, srv, http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Errorf("%s should be public, got %d (body: %s)", path, w.Code, w.Body.String())
		}
	}
}

func TestClerkAuth_AttachesUserIDToContext(t *testing.T) {
	f := newJWKSFixture(t)

	// Build a minimal middleware-only test: register a probe handler that
	// pulls the user_id off context and writes it back, run it under the
	// auth middleware, verify the round-trip.
	authMW, err := clerkAuth(f.issuer(), discardLogger())
	if err != nil {
		t.Fatalf("clerkAuth: %v", err)
	}
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(UserIDFromContext(r.Context())))
	})
	wrapped := authMW(probe)

	tok := f.signToken(t, f.issuer(), "user_marfa_lights", time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "user_marfa_lights" {
		t.Errorf("user id: want %q, got %q", "user_marfa_lights", got)
	}
}

func TestClerkAuth_AcceptsLowercaseBearerScheme(t *testing.T) {
	f := newJWKSFixture(t)
	srv := mustNewServer(t, Config{ClerkIssuer: f.issuer()})
	tok := f.signToken(t, f.issuer(), "user_skunk_ape", time.Now().Add(time.Hour))

	// RFC 6750 §2.1 declares the auth scheme case-insensitive; some HTTP
	// libraries lowercase by convention.
	req := httptest.NewRequest(http.MethodGet, "/v1/configs", nil)
	req.Header.Set("Authorization", "bearer "+tok)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("lowercase bearer: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_AllowsClockSkew(t *testing.T) {
	f := newJWKSFixture(t)
	srv := mustNewServer(t, Config{ClerkIssuer: f.issuer()})

	// Token expired 10 seconds ago — should still be accepted under the
	// 30-second clock-skew leeway.
	tok := f.signToken(t, f.issuer(), "user_chessie", time.Now().Add(-10*time.Second))
	w := doAuthed(t, srv, http.MethodGet, "/v1/configs", tok)
	if w.Code != http.StatusOK {
		t.Errorf("recently-expired token within leeway: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestClerkAuth_FailsFastOnUnreachableJWKS(t *testing.T) {
	// Issuer points to nothing — JWKS fetch should fail and New should
	// return an error rather than serving requests it can't authenticate.
	_, err := New(Config{ClerkIssuer: "http://127.0.0.1:1"}, nil)
	if err == nil {
		t.Fatal("expected New to return an error when JWKS is unreachable")
	}
	if !strings.Contains(err.Error(), "JWKS") {
		t.Errorf("error should mention JWKS, got %v", err)
	}
}

// --- helpers ---

func doAuthed(t *testing.T, srv *Server, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// discardLogger returns a slog.Logger that drops everything. Used by
// tests that exercise middleware in isolation and don't want noise in
// `go test -v` output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
