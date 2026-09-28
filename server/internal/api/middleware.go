package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"
)

// debugPaths contains path prefixes that should be logged at Debug level
// to avoid noise from high-frequency polling and long-lived SSE connections.
var debugPaths = []string{
	"/api/hooks/pending",
	"/api/system/health",
	"/api/system",
	"/api/agents/stream",
	"/api/tasks/stream",
}

func isDebugPath(path string) bool {
	for _, prefix := range debugPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// SlogMiddleware logs each request with method, path, status, and duration.
// High-frequency and background paths (health checks, SSE streams, polling)
// are logged at Debug level; everything else at Info.
func SlogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start),
			"requestID", chimiddleware.GetReqID(r.Context()),
		}
		if isDebugPath(r.URL.Path) {
			slog.Debug("request", args...)
		} else {
			slog.Info("request", args...)
		}
	})
}

// RequireSameOriginForMutations blocks cross-origin mutation requests.
// It checks the Origin or Referer header for non-GET/HEAD/OPTIONS requests
// and rejects any that come from a different origin than the server itself.
// This defends against drive-by CSRF in the local loopback trust model.
//
// The middleware runs unconditionally for all mutation methods — there is no
// exemption for Bearer-token requests. Bearer-only paths (MCP, hooks,
// channel-reply) are mounted outside this middleware's scope, so they are
// unaffected.
func RequireSameOriginForMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header — check Referer as fallback.
			origin = r.Header.Get("Referer")
		}
		// Fail-closed: if both Origin and Referer are absent for a mutating
		// request, deny it — there is no way to verify the request source.
		if origin == "" {
			http.Error(w, `{"error":"missing Origin header"}`, http.StatusForbidden)
			return
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host != r.Host {
			http.Error(w, `{"error":"cross-origin request denied"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHosts is the set of host names (without port) that are considered
// loopback addresses and are therefore allowed through RequireLoopbackHost.
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
}

// RequireLoopbackHostConfig holds optional configuration for RequireLoopbackHost.
// The zero value is safe to use: it enforces the built-in loopback whitelist.
type RequireLoopbackHostConfig struct {
	// ExtraAllowedHosts lists additional Host values (without port) that are
	// permitted beyond the built-in loopback set. Useful for multi-machine setups
	// reached via a VPN or SSH tunnel where a non-loopback hostname is stable.
	// Values are matched case-insensitively after stripping the port.
	ExtraAllowedHosts []string
}

// RequireLoopbackHost returns middleware that rejects requests whose Host header
// does not resolve to a loopback address. This closes the DNS-rebinding vector:
// a malicious page hosted on a public domain cannot instruct the browser to
// send same-origin requests to 127.0.0.1 because the Host header will carry the
// attacker-controlled domain name, not the loopback address.
//
// The whitelist is: 127.0.0.1, localhost, [::1], plus any hosts in cfg.ExtraAllowedHosts.
// Requests without a Host header (HTTP/1.0 clients) are allowed through unchanged —
// they cannot be initiated by a browser, so they are not a rebinding risk.
// Rejected requests receive 403 Forbidden with a JSON error body.
func RequireLoopbackHost(cfg RequireLoopbackHostConfig) func(http.Handler) http.Handler {
	// Build the effective allow-set at construction time so the hot path is O(1).
	allowed := make(map[string]bool, len(loopbackHosts)+len(cfg.ExtraAllowedHosts))
	for h := range loopbackHosts {
		allowed[h] = true
	}
	for _, h := range cfg.ExtraAllowedHosts {
		allowed[strings.ToLower(strings.TrimSpace(h))] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if host == "" {
				// No Host header — HTTP/1.0 or direct TCP; not a browser, allow through.
				next.ServeHTTP(w, r)
				return
			}
			// Strip port if present.
			h, _, err := net.SplitHostPort(host)
			if err != nil {
				// net.SplitHostPort returns an error when there is no port; use the raw value.
				h = host
			}
			if !allowed[strings.ToLower(h)] {
				http.Error(w, `{"error":"forbidden: host not in loopback whitelist"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ipLimiter holds a per-IP token-bucket rate limiter and its last-seen time
// so the cleanup goroutine can evict stale entries.
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiterConfig configures the per-IP token-bucket middleware.
type IPRateLimiterConfig struct {
	// Rate is the sustained request rate per second per IP. Default: 10.
	Rate rate.Limit
	// Burst is the maximum burst size per IP. Default: 120.
	//
	// Sized from measurement, not from intuition: one cold start of the SPA
	// issues 21 API requests with a peak of 20 in a single second, and the
	// server binds loopback only, so every client shares the 127.0.0.1 bucket —
	// two windows open at once peak at 40. 120 leaves room for a third and
	// fourth client while the sustained rate still bounds a probing loop.
	Burst int
	// CleanupInterval controls how often stale limiter entries are evicted.
	// Default: 5 minutes.
	CleanupInterval time.Duration
	// StaleAfter is the idle period after which an IP entry is considered stale
	// and eligible for eviction. Default: 10 minutes.
	StaleAfter time.Duration
}

func (c *IPRateLimiterConfig) applyDefaults() {
	if c.Rate <= 0 {
		c.Rate = 10
	}
	if c.Burst <= 0 {
		c.Burst = 120
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = 5 * time.Minute
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = 10 * time.Minute
	}
}

// NewIPRateLimiter returns middleware that enforces a per-remote-IP token-bucket
// rate limit. When the limit is exceeded the handler responds with 429 Too Many
// Requests and a Retry-After header indicating when the client may retry.
//
// Apply this to high-cost endpoints such as auth, MCP, and bulk-resolve to
// prevent SHA-256 amplification attacks and auth-probing DoS.
//
// ctx controls the lifetime of the background cleanup goroutine. Pass
// context.Background() in production or a test-scoped context in tests.
func NewIPRateLimiter(ctx context.Context, cfg IPRateLimiterConfig) func(http.Handler) http.Handler {
	cfg.applyDefaults()

	var mu sync.Mutex
	limiters := make(map[string]*ipLimiter)

	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		entry, ok := limiters[ip]
		if !ok {
			entry = &ipLimiter{limiter: rate.NewLimiter(cfg.Rate, cfg.Burst)}
			limiters[ip] = entry
		}
		entry.lastSeen = time.Now()
		return entry.limiter
	}

	// Background cleanup to prevent unbounded growth of the limiter map.
	// Exits when ctx is cancelled (e.g. on server shutdown or in tests).
	go func() {
		ticker := time.NewTicker(cfg.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-cfg.StaleAfter)
				mu.Lock()
				for ip, entry := range limiters {
					if entry.lastSeen.Before(cutoff) {
						delete(limiters, ip)
					}
				}
				mu.Unlock()
			}
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// r.RemoteAddr is set by chi's RealIP middleware to the real client IP.
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			lim := getLimiter(ip)
			// Single ReserveN call: avoids the double-consume race between Allow()
			// and Reserve() that would silently burn an extra token on denial.
			res := lim.ReserveN(time.Now(), 1)
			if !res.OK() || res.Delay() > 0 {
				res.Cancel() // refund the reservation
				retryAfter := int(res.Delay().Seconds()) + 1
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// StripForwardedHeaders removes proxy-injected forwarding headers from every
// inbound request. Since the dashboard binds exclusively to loopback (127.0.0.1)
// these headers should never be present, and trusting them would allow an
// attacker on the local machine to spoof origin metadata understood by other
// middleware (e.g. CORS checks) or the client IP recorded for a request.
// Stripping them here, as the very first middleware in the chain, ensures no
// downstream handler ever sees them and RemoteAddr always reflects the real
// socket peer.
//
// Headers stripped: X-Forwarded-Host, X-Forwarded-Proto, Forwarded,
// X-Forwarded-For, X-Real-IP.
func StripForwardedHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-Proto")
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Real-IP")
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders sets security-relevant HTTP response headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// style-src 'self' (no 'unsafe-inline'): the production Vite build extracts all
		// SFC styles to linked .css files — no <style> tags are injected at runtime.
		// If a third-party library that injects inline styles is added in the future,
		// prefer SHA-256 hashes over re-adding 'unsafe-inline'.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https://avatars.githubusercontent.com; connect-src 'self'; font-src 'self' data:; object-src 'none'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
