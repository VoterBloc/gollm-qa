package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// clockSkewLeeway tolerates small clock differences between the panel
// (token issuer side) and the engine (token validator side). 30s
// matches what most JWT libraries default to and what Auth0 / Clerk's
// own server SDKs use.
const clockSkewLeeway = 30 * time.Second

// userIDKey is the request-context key under which validated Clerk user
// ids are stored. Unexported (using a typed key) so other packages can't
// accidentally read or write it; access goes through UserIDFromContext.
type userIDCtxKey struct{}

// UserIDFromContext returns the Clerk user id (`sub` claim) attached by
// the auth middleware, or empty string if no user was validated for this
// request. Empty is returned in dev mode (auth disabled) and on the
// public endpoints (/health, /openapi.json).
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// publicAuthPaths bypass JWT validation even when Clerk is configured.
// /health is the liveness probe; /openapi.json is the spec consumed by
// codegen tools that may not have a session.
var publicAuthPaths = map[string]bool{
	"/health":       true,
	"/openapi.json": true,
}

// clerkAuth returns middleware that validates Clerk-issued JWTs. When
// issuer is empty, returns a no-op middleware (dev mode) — useful for
// the existing CLI workflow that doesn't involve a panel.
//
// In Clerk mode, the middleware fetches the issuer's JWKS at startup
// and caches it; key rotations are picked up automatically when an
// unknown `kid` shows up. Validation rejects on missing Bearer header,
// wrong issuer, expired tokens, malformed tokens, or signature
// mismatch. Validated requests have the `sub` claim attached to the
// context as the user id.
func clerkAuth(issuer string, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	if issuer == "" {
		logger.Info("auth disabled (dev mode); set --clerk-issuer to enable")
		return func(next http.Handler) http.Handler { return next }, nil
	}

	issuer = strings.TrimRight(issuer, "/")
	jwksURL := issuer + "/.well-known/jwks.json"

	// Surface a security-relevant footgun: a production deploy with a
	// plaintext issuer URL would have its JWKS fetched in the clear.
	// Loopback addresses are exempt because tests use them.
	warnIfPlaintextNonLoopback(issuer, logger)

	// Probe the JWKS endpoint up front so misconfiguration (typos in
	// the issuer URL, wrong protocol, etc.) fails at startup instead of
	// on the first request. keyfunc.NewDefault by itself runs the
	// initial fetch in the background and only logs on failure.
	if err := probeJWKS(jwksURL); err != nil {
		return nil, fmt.Errorf("verifying JWKS at %s: %w", jwksURL, err)
	}

	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("setting up JWKS client for %s: %w", jwksURL, err)
	}

	logger.Info("auth=clerk", "issuer", issuer)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicAuthPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			raw, ok := stripBearer(r.Header.Get("Authorization"))
			if !ok || raw == "" {
				logger.Info("auth rejected", "reason", "missing-bearer", "path", r.URL.Path)
				writeAuthError(w, "missing bearer token")
				return
			}

			token, err := jwt.Parse(raw, k.Keyfunc,
				jwt.WithIssuer(issuer),
				jwt.WithValidMethods([]string{"RS256"}),
				jwt.WithExpirationRequired(),
				jwt.WithLeeway(clockSkewLeeway),
			)
			if err != nil {
				logger.Info("auth rejected", "reason", classifyJWTError(err), "path", r.URL.Path)
				writeAuthError(w, "invalid token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				logger.Info("auth rejected", "reason", "claims-not-map", "path", r.URL.Path)
				writeAuthError(w, "invalid claims")
				return
			}
			sub, _ := claims["sub"].(string)
			if sub == "" {
				logger.Info("auth rejected", "reason", "missing-sub", "path", r.URL.Path)
				writeAuthError(w, "invalid claims")
				return
			}

			ctx := context.WithValue(r.Context(), userIDCtxKey{}, sub)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}

// classifyJWTError maps jwt.Parse's error tree to a short reason tag for
// structured logging. Tags are stable (never include token contents).
func classifyJWTError(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return "expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return "not-yet-valid"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return "invalid-issuer"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "bad-signature"
	case errors.Is(err, jwt.ErrTokenMalformed):
		return "malformed"
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return "unverifiable"
	default:
		return "parse-failed"
	}
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// stripBearer returns the token portion of an "Authorization: Bearer
// <token>" header value. The scheme match is case-insensitive per RFC
// 6750 §2.1, so `bearer ` and `BEARER ` both work.
func stripBearer(authHeader string) (string, bool) {
	if len(authHeader) <= len("Bearer ") {
		return "", false
	}
	if !strings.EqualFold(authHeader[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	return authHeader[len("Bearer "):], true
}

// warnIfPlaintextNonLoopback logs a warning if the issuer URL uses
// http:// to a non-loopback host. JWKS over plaintext on the public
// internet is MITM-able; in dev, loopback addresses are exempt.
func warnIfPlaintextNonLoopback(issuer string, logger *slog.Logger) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "http" {
		return
	}
	host := u.Hostname()
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return
	}
	logger.Warn("clerk-issuer uses http:// to a non-loopback host — JWKS will be fetched in plaintext", "issuer", issuer)
}

// probeJWKS does a one-shot GET on the JWKS URL with a short timeout
// and verifies the response shape. Lets startup fail clearly when the
// issuer is misconfigured rather than letting first-request validation
// fail with a vague error.
func probeJWKS(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var body struct {
		Keys []any `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decoding JWKS: %w", err)
	}
	if len(body.Keys) == 0 {
		return errors.New("JWKS contains no keys")
	}
	return nil
}
