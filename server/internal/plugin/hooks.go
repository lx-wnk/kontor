package plugin

import (
	authpkg "github.com/lx-wnk/kontor/server/internal/auth"
)

// Hooks allows callers to react to capabilities discovered during Load.
// Each field is optional — nil hooks are silently skipped.
// Add a new field here when a new plugin capability needs server-side wiring.
type Hooks struct {
	// SetAuth is called when an auth_provider plugin passes health-check.
	// loginURL is the plugin's login endpoint (base URL + /login) that
	// core redirects to when starting the OAuth flow.
	SetAuth func(provider authpkg.OAuthProvider, loginURL string)
	// OnUnhealthy is called when a plugin exhausts its restart budget. The entry
	// is retained (so the dispatcher can answer 503); the callback lets the
	// server persist the dead state (e.g. mark the plugin inactive in the DB).
	OnUnhealthy func(id string)
	// future capabilities: SetLLM, SetStorage, SetNotify, etc.
}
