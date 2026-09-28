// Package spawners implements admin-only CRUD endpoints for custom spawners.
package spawners

import (
	"regexp"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// envKeyForbidden matches shell metacharacters and control chars that must
// never appear in an env key.
var envKeyForbidden = regexp.MustCompile("[;&|$`\n\r\t ]")

const envValueMaxLen = 4096

// ValidAdapterTypes is the application-layer allow-list of adapter_type values.
// The ent schema does not enforce this (string column with default "claude");
// validation lives here and at the repo entry points.
var ValidAdapterTypes = []string{"claude", "ollama", "openai", "custom", "acp"}

// allowedAdapterConfigKeys lists permitted adapter_config keys per adapter_type.
// Required keys are listed in requiredAdapterConfigKeys below.
//
// - claude:  optional effort; everything else flows via command/args/env.
// - ollama:  optional host, default_model.
// - openai:  required api_key_env; optional base_url, default_model.
// - custom:  no adapter_config keys; reuses the row's command/args/env.
// - acp:     optional command, args.
//
// This is the server-side acceptance list; llmadapter.AvailableAdapters is a
// separate UI-metadata catalog consumed by the settings form and the
// /api/adapters response. The two are hand-kept in parity — a key added here
// without a matching ConfigKeyDoc entry saves but never renders a control.
var allowedAdapterConfigKeys = map[string]map[string]struct{}{
	"claude": {
		"effort": {},
	},
	"ollama": {
		"host":          {},
		"default_model": {},
	},
	"openai": {
		"api_key_env":   {},
		"base_url":      {},
		"default_model": {},
	},
	"custom": {},
	"acp": {
		"command": {},
		"args":    {},
	},
}

var requiredAdapterConfigKeys = map[string][]string{
	"openai": {"api_key_env"},
}

// adapterConfigCommandKeys lists, per adapter_type, the adapter_config keys whose
// value is an executable and must pass the same trust check as the row's command
// column (services.ValidateSpawnerCommand). Data-driven so a future adapter that
// adds a command-valued key cannot silently skip the check.
var adapterConfigCommandKeys = map[string]map[string]struct{}{
	"acp": {"command": {}},
}

// adapterConfigArgsKeys lists, per adapter_type, the adapter_config key whose
// value is a whitespace-separated argv appended to that type's command key
// (see adapterConfigCommandKeys) at spawn time. Every token that looks like a
// filesystem path is checked like a command; data-driven for the same reason
// as adapterConfigCommandKeys.
var adapterConfigArgsKeys = map[string]string{
	"acp": "args",
}

// ValidateAdapterType returns "", true when t is one of ValidAdapterTypes,
// otherwise an error message suitable for a 400 response.
func ValidateAdapterType(t string) (string, bool) {
	for _, v := range ValidAdapterTypes {
		if t == v {
			return "", true
		}
	}
	return "adapter_type must be one of claude|ollama|openai|custom|acp", false
}

// ValidateAdapterConfig enforces:
//   - adapterType must be a known type
//   - every key must be in the allow-list for the given adapter_type
//   - every required key must be present and non-empty
//   - keys must be non-empty, free of shell metacharacters
//   - values must be <= 4096 chars
//   - command-valued keys (adapterConfigCommandKeys), if non-empty, must pass
//     services.ValidateSpawnerCommand
//   - the args key (adapterConfigArgsKeys), if non-empty, has every
//     path-shaped token pass the same check (see validateArgsPathTokens)
//   - the claude adapter's effort key, if non-empty, must be one of
//     services.ValidEffortLevels
func ValidateAdapterConfig(adapterType string, cfg map[string]string) (string, bool) {
	allowed, ok := allowedAdapterConfigKeys[adapterType]
	if !ok {
		return "adapter_type must be one of claude|ollama|openai|custom|acp", false
	}
	for k, v := range cfg {
		if k == "" {
			return "adapter_config key must be non-empty", false
		}
		if envKeyForbidden.MatchString(k) {
			return "adapter_config key contains forbidden characters", false
		}
		if _, ok := allowed[k]; !ok {
			return "unknown adapter_config key: " + k, false
		}
		if len(v) > envValueMaxLen {
			return "adapter_config value exceeds 4096 chars", false
		}
		if adapterType == "claude" && k == services.AdapterConfigEffortKey && v != "" && !services.IsValidEffortLevel(v) {
			return "adapter_config." + k + ": unrecognised effort level " + v, false
		}
		if _, isCommand := adapterConfigCommandKeys[adapterType][k]; isCommand && v != "" {
			if ok, reason := services.ValidateSpawnerCommand(v); !ok {
				return "adapter_config." + k + ": " + reason, false
			}
		}
		if argsKey, hasArgsKey := adapterConfigArgsKeys[adapterType]; hasArgsKey && k == argsKey && v != "" {
			if reason, ok := validateArgsPathTokens(v); !ok {
				return "adapter_config." + k + ": " + reason, false
			}
		}
	}
	for _, req := range requiredAdapterConfigKeys[adapterType] {
		if v, ok := cfg[req]; !ok || v == "" {
			return "adapter_config missing required key: " + req, false
		}
	}
	return "", true
}

// looksLikeArgsPathToken reports whether tok is shaped like a filesystem path
// (absolute or relative) rather than a flag or a package spec.
func looksLikeArgsPathToken(tok string) bool {
	return strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../")
}

// validateArgsPathTokens splits args the same way the spawner does
// (strings.Fields) and runs every path-shaped token through
// services.ValidateSpawnerCommand, the same trust check a command value gets.
// ponytail: a bare filename (resolved against the child's cwd, which is
// already operator-controlled) and a remote package spec such as `npx
// <tarball-url>` (the shape the documented ACP default itself uses) are not
// covered by this check.
func validateArgsPathTokens(args string) (string, bool) {
	for _, tok := range strings.Fields(args) {
		if !looksLikeArgsPathToken(tok) {
			continue
		}
		if ok, reason := services.ValidateSpawnerCommand(tok); !ok {
			return reason, false
		}
	}
	return "", true
}

// ValidateSlug returns true when s is a valid slug.
// Delegates to the canonical validation package — do not define a local copy.
func ValidateSlug(s string) bool {
	return validation.IsValidSlug(s)
}

// ValidateEnv enforces:
//   - key non-empty
//   - key contains no shell metacharacters or whitespace/newlines
//   - value length <= 4096
func ValidateEnv(env map[string]string) (string, bool) {
	for k, v := range env {
		if k == "" {
			return "env key must be non-empty", false
		}
		if envKeyForbidden.MatchString(k) {
			return "env key contains forbidden characters", false
		}
		if len(v) > envValueMaxLen {
			return "env value exceeds 4096 chars", false
		}
	}
	return "", true
}
