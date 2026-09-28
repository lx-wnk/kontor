package cli

import (
	"context"
	"fmt"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newGrantsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "grants", Short: "Manage capability grants directly in the DB (direct DB access)"}
	cmd.PersistentFlags().String("db", "", "Path to the dashboard SQLite DB (default: $DASHBOARD_DB_PATH or ~/.claude/dashboard-tasks.db)")

	add := &cobra.Command{Use: "add <capability>", Short: "Create a grant for a capability", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		opts := grantAddOpts{}
		opts.Scope, _ = cmd.Flags().GetString("scope")
		opts.Mode, _ = cmd.Flags().GetString("mode")
		opts.Pattern, _ = cmd.Flags().GetString("pattern")
		opts.PatternSet = cmd.Flags().Changed("pattern")
		opts.LimitCount, _ = cmd.Flags().GetInt("limit-count")
		opts.LimitWindow, _ = cmd.Flags().GetInt("limit-window")
		opts.ExpiresIn, _ = cmd.Flags().GetString("expires-in")
		opts.GrantedBy, _ = cmd.Flags().GetString("granted-by")
		opts.Reason, _ = cmd.Flags().GetString("reason")

		return withStore(cmd, func(ctx context.Context, s *dbStore) error {
			g, err := addGrant(ctx, s, args[0], opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "granted %s: capability=%s mode=%s scope=%s pattern=%s\n",
				g.ID, g.CapabilityName, g.Mode, scopeString(g.ContextKind, g.ContextRef), patternOrWildcard(g.Pattern))
			// Grants have exactly one production reader (memory.Authorize); spawn
			// builds its allow-list from task_permissions and hook runs its own
			// matcher, so neither ever queries this table.
			if c, err := s.caps.Get(ctx, g.CapabilityName); err == nil && !isServerEnforceable(c) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: no enforcement point reads stored grants for capability %q today — the spawn point builds its allow-list from task_permissions, and the hook point runs its own matcher; the grant is recorded and will apply once a reader exists\n", g.CapabilityName)
			}
			return nil
		})
	}}
	add.Flags().String("scope", repo.GrantContextGlobal, "Context to grant in: kind or kind:ref (e.g. project:/home/x)")
	add.Flags().String("mode", repo.GrantModeAllow, "Grant mode: allow, deny, ask")
	add.Flags().String("pattern", "", `Pattern to match — required; pass "*" to grant every value, or a specific pattern (prefix patterns end in *)`)
	add.Flags().Int("limit-count", 0, "Rate limit count (0 = unlimited)")
	add.Flags().Int("limit-window", 0, "Rate limit window, in seconds")
	add.Flags().String("expires-in", "", "Expiry as a Go duration (e.g. 24h, 30m); empty = never expires")
	add.Flags().String("granted-by", "", "Who is granting this (default: cli:<current os user>)")
	add.Flags().String("reason", "", "Why this grant is being made")

	list := &cobra.Command{Use: "list", Short: "List grants", RunE: func(cmd *cobra.Command, _ []string) error {
		capFilter, _ := cmd.Flags().GetString("capability")
		asJSON, _ := cmd.Flags().GetBool("json")
		return withStore(cmd, func(ctx context.Context, s *dbStore) error {
			var rows []*ent.Grant
			var err error
			if capFilter != "" {
				rows, err = s.grants.ListForCapability(ctx, capFilter)
			} else {
				rows, err = s.grants.List(ctx)
			}
			if err != nil {
				return err
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].GrantedAt.After(rows[j].GrantedAt) })
			if asJSON {
				return printJSON(rows)
			}
			caps, err := s.caps.List(ctx)
			if err != nil {
				return err
			}
			printGrantsTable(rows, enforcementByCapability(caps))
			return nil
		})
	}}
	list.Flags().String("capability", "", "Filter by capability name")
	list.Flags().Bool("json", false, "Print as JSON instead of a table")

	revoke := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a grant",
		Long:  "Revoke a grant. This tombstones the row (sets revoked_at/revoked_by) rather than deleting it, so grant history stays intact.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			revokedBy, _ := cmd.Flags().GetString("revoked-by")
			if revokedBy == "" {
				revokedBy = defaultActor()
			}
			return withStore(cmd, func(ctx context.Context, s *dbStore) error {
				if err := s.grants.Revoke(ctx, args[0], revokedBy); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked %s\n", args[0])
				return nil
			})
		},
	}
	revoke.Flags().String("revoked-by", "", "Who is revoking this (default: cli:<current os user>)")

	capabilities := &cobra.Command{Use: "capabilities", Short: "List the capability catalogue", RunE: func(cmd *cobra.Command, _ []string) error {
		return withStore(cmd, func(ctx context.Context, s *dbStore) error {
			repo.SeedCapabilities(ctx, s.caps) // never-opened DB has an empty catalogue; add's lookup would fail every name without this
			rows, err := s.caps.List(ctx)
			if err != nil {
				return err
			}
			w := newTabWriter()
			fmt.Fprintln(w, "NAME\tCLASS\tENFORCEABLE-BY")
			for _, c := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.Class, strings.Join(c.EnforceableBy, ","))
			}
			return w.Flush()
		})
	}}

	cmd.AddCommand(add, list, revoke, capabilities)
	return cmd
}

// grantAddOpts mirrors add's flags so addGrant can be called directly from
// tests without going through cobra.
type grantAddOpts struct {
	Scope       string
	Mode        string
	Pattern     string
	PatternSet  bool
	LimitCount  int
	LimitWindow int
	ExpiresIn   string
	GrantedBy   string
	Reason      string
}

// addGrant seeds the capability catalogue, resolves capName against it, and
// creates the grant. A capability name unknown to the catalogue is rejected
// here rather than left to Create: an unknown name has no catalogue row, so
// capability.Decide would resolve it to a zero-value CapabilityView (empty
// class) and deny forever — the grant would write successfully and then never
// take effect.
func addGrant(ctx context.Context, s *dbStore, capName string, opts grantAddOpts) (*ent.Grant, error) {
	// Three defaults (scope, mode, pattern) would otherwise compound silently
	// into the widest grant the system can express; require the caller to say
	// what they mean.
	if !opts.PatternSet {
		return nil, fmt.Errorf("--pattern is required; pass --pattern '*' to grant every value, or a specific pattern (prefix patterns end in *, e.g. 'git status*')")
	}

	repo.SeedCapabilities(ctx, s.caps) // never-opened DB has an empty catalogue; this is the only place that seeds it before a grant is written

	if _, err := s.caps.Get(ctx, capName); err != nil {
		if repo.IsNotFound(err) {
			return nil, fmt.Errorf("unknown capability %q — run `agent-dashboard grants capabilities` to list valid names", capName)
		}
		return nil, err
	}

	gctx, err := parseGrantScope(opts.Scope)
	if err != nil {
		return nil, fmt.Errorf("--scope: %w", err)
	}

	// A zero window is counted as "usages since now", which is always none, so
	// a limit paired with it never triggers and reads as unlimited.
	if opts.LimitCount > 0 && opts.LimitWindow <= 0 {
		return nil, fmt.Errorf("--limit-window must be greater than 0 when --limit-count is set, or the limit never triggers")
	}

	var expiresAt *time.Time
	if opts.ExpiresIn != "" {
		d, err := time.ParseDuration(opts.ExpiresIn)
		if err != nil {
			return nil, fmt.Errorf("--expires-in: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("--expires-in must be a positive duration, got %q", opts.ExpiresIn)
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	grantedBy := opts.GrantedBy
	if grantedBy == "" {
		grantedBy = defaultActor()
	}

	return s.grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName:     capName,
		Context:            gctx,
		Pattern:            opts.Pattern,
		Mode:               opts.Mode,
		LimitCount:         opts.LimitCount,
		LimitWindowSeconds: opts.LimitWindow,
		ExpiresAt:          expiresAt,
		GrantedBy:          grantedBy,
		Reason:             opts.Reason,
	})
}

// parseGrantScope splits on the first colon only, so a ref containing a colon
// (e.g. a task id) is not truncated.
func parseGrantScope(s string) (repo.GrantContext, error) {
	parts := strings.SplitN(s, ":", 2)
	kind := parts[0]
	ref := ""
	if len(parts) == 2 {
		ref = parts[1]
	}
	if !capability.IsValidContextKind(kind) {
		return repo.GrantContext{}, fmt.Errorf("invalid scope kind %q (valid: %s)", kind, strings.Join(capability.ContextKinds(), ", "))
	}
	return repo.GrantContextFor(kind, ref), nil
}

// defaultActor is the fallback --granted-by / --revoked-by value: the current
// OS user, or "cli:unknown" if it cannot be resolved. GrantRepo.Create and
// Revoke both reject an empty actor, so a fallback is required, not optional.
func defaultActor() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "cli:unknown"
	}
	return "cli:" + u.Username
}

func printGrantsTable(rows []*ent.Grant, enforcement map[string]string) {
	w := newTabWriter()
	fmt.Fprintln(w, "ID\tCAPABILITY\tMODE\tSCOPE\tPATTERN\tEXPIRES\tSTATUS\tENFORCEMENT\tGRANTED-BY")
	for _, g := range rows {
		enf, ok := enforcement[g.CapabilityName]
		if !ok {
			enf = "unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			g.ID, g.CapabilityName, g.Mode, scopeString(g.ContextKind, g.ContextRef),
			quoteIfControl(patternOrWildcard(g.Pattern)), formatExpires(g.ExpiresAt), grantStatus(g),
			enf, quoteIfControl(g.GrantedBy))
	}
	w.Flush()
}

// isServerEnforceable reports whether c is read by the one production
// enforcement point that queries stored grants (internal/memory.Authorize).
func isServerEnforceable(c *ent.Capability) bool {
	for _, e := range c.EnforceableBy {
		if e == capability.EnforcerServer {
			return true
		}
	}
	return false
}

// enforcementByCapability maps a capability name to "server" or "none" for
// the list table's ENFORCEMENT column, built once from the whole catalogue
// rather than queried per row.
func enforcementByCapability(caps []*ent.Capability) map[string]string {
	m := make(map[string]string, len(caps))
	for _, c := range caps {
		if isServerEnforceable(c) {
			m[c.Name] = capability.EnforcerServer
		} else {
			m[c.Name] = "none"
		}
	}
	return m
}

// quoteIfControl guards against a stored pattern or actor value carrying a
// terminal control sequence (patterns can originate from an unsanitized
// permission request) that would otherwise rewrite prior lines in the table.
func quoteIfControl(s string) string {
	for _, r := range s {
		if unicode.IsControl(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

func scopeString(kind, ref string) string {
	if ref == "" {
		return kind
	}
	return kind + ":" + ref
}

func patternOrWildcard(pattern string) string {
	if pattern == "" {
		return "*"
	}
	return pattern
}

func formatExpires(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func grantStatus(g *ent.Grant) string {
	if g.RevokedAt != nil {
		return "revoked"
	}
	if g.ExpiresAt != nil && g.ExpiresAt.Before(time.Now()) {
		return "expired"
	}
	return "active"
}
