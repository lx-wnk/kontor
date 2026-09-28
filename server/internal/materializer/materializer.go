package materializer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// LeaseNamespace is the coord_lock namespace holding one lease per node. Two
// dashboard instances on one machine would otherwise write the same files, and
// reachability is not ownership — this project already learned that when a
// second desktop instance adopted a foreign server because a health check
// returned 200.
const LeaseNamespace = "materialize"

// DefaultLeaseTTL bounds how long a crashed run keeps the node locked.
// coord_lock expiry is lazy: there is no sweeper and an expired row survives
// until the same key is re-acquired (repo/coord_lock_repo.go:73,109-114).
// Harmless here — the next run is what re-acquires it.
const DefaultLeaseTTL = 2 * time.Minute

// ReportEntry is what happened at one (resource, target) pair.
type ReportEntry struct {
	ResourceID string  `json:"resourceId"`
	Slug       string  `json:"slug"`
	Provider   string  `json:"provider"`
	TargetKey  string  `json:"targetKey"`
	Path       string  `json:"path,omitempty"`
	Outcome    Outcome `json:"outcome"`
	Detail     string  `json:"detail,omitempty"`
}

// Report is one run. It states what it did not do as explicitly as what it
// did: a materializer that reports only its writes is one whose refusals are
// invisible.
type Report struct {
	NodeID      string `json:"nodeId"`
	Owner       string `json:"owner"`
	DryRun      bool   `json:"dryRun"`
	Leased      bool   `json:"leased"`
	LeaseHolder string `json:"leaseHolder,omitempty"`
	// Partial is true when at least one target failed. Other targets still
	// proceeded; the run is neither a success nor a failure.
	Partial bool `json:"partial"`
	// Targets lists every target key considered, so a resource that resolved
	// to none is distinguishable from one that had nothing to do.
	Targets []string      `json:"targets"`
	Entries []ReportEntry `json:"entries"`
}

// Materializer produces the files agent runtimes read from skill resources.
type Materializer struct {
	Resources repo.ResourceRepo
	Skills    repo.SkillRepo
	Records   repo.MaterializationRepo
	Locks     repo.CoordLockRepo
	Resolver  Resolver
	NodeID    string
	// Owner identifies this process to the lease. Per process, not per run:
	// coord_lock.Acquire is re-entrant for the same owner
	// (repo/coord_lock_repo.go:73), so two concurrent runs in one process would
	// both hold it — which is why the HTTP handler single-flights on top.
	Owner    string
	LeaseTTL time.Duration
}

// New builds a Materializer with a fresh per-process lease owner.
func New(
	resources repo.ResourceRepo,
	skills repo.SkillRepo,
	records repo.MaterializationRepo,
	locks repo.CoordLockRepo,
	resolver Resolver,
) *Materializer {
	return &Materializer{
		Resources: resources,
		Skills:    skills,
		Records:   records,
		Locks:     locks,
		Resolver:  resolver,
		NodeID:    resolver.NodeID,
		Owner:     "materializer:" + uuid.NewString(),
		LeaseTTL:  DefaultLeaseTTL,
	}
}

// Run classifies every enabled skill resource on this node against every
// target and writes the ones it owns.
//
// A dry run takes no lease: it writes nothing, so serialising it would only
// stop two people reading the same report at once. A writing run that cannot
// take the lease degrades to read-only and names the holder — it still reports
// exactly what it would have written.
func (m *Materializer) Run(ctx context.Context, dryRun bool) (Report, error) {
	rep := Report{
		NodeID:  m.NodeID,
		Owner:   m.Owner,
		DryRun:  dryRun,
		Targets: []string{},
		Entries: []ReportEntry{},
	}

	readOnly := "dry run"
	if !dryRun {
		ok, holder, _, err := m.Locks.Acquire(ctx, LeaseNamespace, m.NodeID, m.Owner, m.LeaseTTL)
		if err != nil {
			return rep, fmt.Errorf("materialize: acquire node lease: %w", err)
		}
		rep.Leased = ok
		if ok {
			readOnly = ""
			defer func() {
				// WithoutCancel: a client that disconnected mid-run must not
				// leave the node locked until the lease expires.
				if rerr := m.Locks.Release(context.WithoutCancel(ctx), LeaseNamespace, m.NodeID, m.Owner); rerr != nil {
					slog.Warn("materialize: release node lease", "node", m.NodeID, "err", rerr)
				}
			}()
		} else {
			rep.LeaseHolder = holder
			readOnly = "node lease held by " + holder
		}
	}

	resources, err := m.Resources.ListForKind(ctx, repo.ResourceKindSkill)
	if err != nil {
		return rep, fmt.Errorf("materialize: list skill resources: %w", err)
	}

	seen := map[string]bool{}
	for _, res := range resources {
		if res.NodeID != m.NodeID {
			continue // cross-node materialization is V2, with the node registry
		}
		if res.State == repo.ResourceStateDisabled || res.State == repo.ResourceStateOrphaned {
			continue
		}

		targets := m.Resolver.Targets(repo.Scope{Kind: repo.ScopeKind(res.ScopeKind), Ref: res.ScopeRef})
		for _, t := range targets {
			if !seen[t.Key()] {
				seen[t.Key()] = true
				rep.Targets = append(rep.Targets, t.Key())
			}
		}

		content, cerr := m.Skills.GetByResource(ctx, res.ID)
		if cerr != nil {
			rep.Entries = append(rep.Entries, ReportEntry{
				ResourceID: res.ID, Slug: res.Slug,
				Outcome: OutcomeFailed,
				Detail:  "no skill content to materialize: " + cerr.Error(),
			})
			continue
		}

		skill := Skill{
			ResourceID:  res.ID,
			Slug:        res.Slug,
			Description: content.Description,
			Body:        content.Body,
		}
		for _, t := range targets {
			rep.Entries = append(rep.Entries, m.one(ctx, skill, t, readOnly))
		}
	}

	sort.Strings(rep.Targets)
	for _, e := range rep.Entries {
		if e.Outcome == OutcomeFailed {
			rep.Partial = true
			break
		}
	}
	return rep, nil
}

// one materializes s into t. readOnly is empty when writing is permitted and
// otherwise names why it is not, so the report says both what would happen and
// what stopped it.
func (m *Materializer) one(ctx context.Context, s Skill, t Target, readOnly string) ReportEntry {
	e := ReportEntry{ResourceID: s.ResourceID, Slug: s.Slug, Provider: t.Provider, TargetKey: t.Key()}

	if t.Adapter == AdapterNone {
		e.Outcome = OutcomeUnsupported
		e.Detail = t.Provider + " has no skill format — not written, and not faked"
		return e
	}

	path, err := SkillPath(t, s.Slug)
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Detail = err.Error()
		return e
	}
	e.Path = path

	rec, err := m.Records.Get(ctx, s.ResourceID, t.Key())
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Detail = err.Error()
		return e
	}
	recordedHash := ""
	if rec != nil {
		recordedHash = rec.ContentHash
	}

	want := RenderClaudeSkill(s)
	outcome, err := Classify(path, want, recordedHash)
	if err != nil {
		e.Outcome = OutcomeFailed
		e.Detail = err.Error()
		return e
	}
	e.Outcome = outcome

	switch outcome {
	case OutcomeUnchanged:
		return e

	case OutcomeConflict:
		e.Detail = "hand-edited since it was written — no merge, no overwrite, and no retry that would overwrite it later"
		return e

	case OutcomeForeign:
		if rec != nil {
			e.Detail = "not ours — reported " + rec.CreatedAt.Format(time.RFC3339) + ", left untouched"
			return e
		}
		if _, rerr := m.Records.Record(ctx, repo.RecordMaterializationInput{
			ResourceID: s.ResourceID, TargetKey: t.Key(), Path: path,
			ContentHash: "", Outcome: string(OutcomeForeign),
		}); rerr != nil {
			e.Detail = "not ours, and the report could not be remembered: " + rerr.Error()
			return e
		}
		e.Detail = "not ours — first seen, left untouched"
		return e
	}

	// created | repaired — the only two outcomes that write.
	if readOnly != "" {
		e.Detail = "not written (" + readOnly + ")"
		return e
	}
	if aerr := Apply(t, path, want); aerr != nil {
		e.Outcome = OutcomeFailed
		e.Detail = aerr.Error()
		return e
	}
	if _, rerr := m.Records.Record(ctx, repo.RecordMaterializationInput{
		ResourceID: s.ResourceID, TargetKey: t.Key(), Path: path,
		ContentHash: HashBytes(want), Outcome: string(outcome),
	}); rerr != nil {
		// A written file with no record reads as foreign on the next run, and
		// this node then stops maintaining a file it wrote itself. Loud.
		e.Outcome = OutcomeFailed
		e.Detail = "written but not recorded — the next run will treat it as foreign: " + rerr.Error()
	}
	return e
}
