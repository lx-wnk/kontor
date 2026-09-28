package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/askgate"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/claudesettings"
	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

const (
	// permissionHoldTimeout is how long a PreToolUse hook call is held while the
	// dashboard waits for a human. It must stay comfortably below the `timeout`
	// configured for the hook in settings: when the hook times out, Claude Code
	// falls back to its own terminal prompt (measured, not assumed), so lapsing
	// first on our side keeps that fallback clean instead of racing it.
	permissionHoldTimeout = 25 * time.Second

	// permissionNoticeTTL bounds how long a "the terminal is asking" notice
	// survives without a refresh. The Notification hook fires once when the
	// prompt opens and never again when it is answered, so the notice has to age
	// out rather than wait to be cleared.
	//
	// Sized for the situation it reports: someone stepped away from a prompt.
	// Two minutes dropped the agent out of the band while it was still blocked,
	// which is the one case the terminal fallback exists for. A stale notice
	// costs a label that is out of date -- it no longer offers a standing grant
	// on its own, which needs the correlation below.
	permissionNoticeTTL = 15 * time.Minute

	// lapseCorrelationWindow is how long after a hold lapses its tool call still
	// explains an incoming terminal-prompt notice. Claude Code draws the prompt
	// as soon as the hook returns no decision, so the Notification follows within
	// a moment; past that the two are unrelated events.
	lapseCorrelationWindow = 10 * time.Second

	// armedTTL bounds how long a session stays armed without being renewed. An
	// arm that is forgotten would otherwise keep holding that session's tool
	// calls for the life of the process.
	armedTTL = 30 * time.Minute

	// maxHoldsPerSession caps concurrent holds for one session. Past the cap the
	// answer is "no decision", which is the same fail-safe a lapse gives, so the
	// overflow degrades to a terminal prompt rather than to unbounded goroutines.
	maxHoldsPerSession = 8

	maxPermissionBodyBytes = 64 * 1024
	// maxPatternRunes bounds the tool argument taken from the hook payload. The
	// value is agent-authored and is displayed next to an approve control, so it
	// is bounded for the eye rather than for the wire.
	maxPatternRunes = 400
)

// heldPermission is the metadata askgate.Store carries for one PreToolUse
// call held open while a human decides.
type heldPermission struct {
	perm      sdk.PendingPermission
	sessionID string
	toolUseID string
	// seq orders holds within one session. RequestedAt has second precision, so
	// two calls from the same batch would otherwise sort arbitrarily and the
	// card could answer a different request than the one it rendered.
	seq uint64
}

// armedSession is one session someone asked the dashboard to intercept.
//
// cwd is the working directory the scanner reported for that session when it
// was armed, and it is the bridge's only identity check: session_id arrives in
// a POST body, so the shared secret alone would let any local process holding
// it raise a card under a trusted agent's name, with an attacker-chosen command
// next to the Allow button. Empty when the scan reported none, in which case
// there is nothing to compare and the check is skipped rather than faked.
type armedSession struct {
	at  time.Time
	cwd string
}

// permissionNotice records that a session's terminal is showing its own
// permission prompt — which happens when no dashboard decision arrived in time,
// or when the bridge hook is not installed at all.
//
// toolUseID names the call the prompt is about, when a hold for this session
// lapsed just before the notice arrived. The Notification payload itself does
// not carry one, so it is empty for a session the bridge never held. It is what
// lets the client offer a standing grant for that call and no other: the trail's
// own pending tool use is derived independently from the transcript and can name
// a different call entirely, so a notice that outlives its prompt would
// otherwise put a grant button next to a tool nobody asked about.
type permissionNotice struct {
	at        time.Time
	toolUseID string
}

// lapse records a hold that ended without a decision, so an incoming notice can
// be attributed to the call that caused it.
type lapse struct {
	at        time.Time
	toolUseID string
}

// HookEnforcer holds PreToolUse hook calls open so a permission prompt can
// be answered in the dashboard instead of in the session's terminal.
//
// Two signals arrive, and they are mutually exclusive by construction:
//
//   - A held PreToolUse call means the decision is still ours to make. Claude
//     Code has not drawn its prompt yet; it is blocked on our answer.
//   - A Notification with notification_type "permission_prompt" means the hold
//     already lapsed (or never happened) and the terminal is now asking. That is
//     no longer answerable from here — it is evidence that someone must go look.
type HookEnforcer struct {
	mu      sync.Mutex
	notices map[string]permissionNotice // session id -> terminal is asking
	lapsed  map[string]lapse            // session id -> last hold that timed out
	// armed holds the sessions whose prompts the dashboard should intercept.
	// PreToolUse fires before Claude Code decides whether to prompt at all, so
	// it carries no signal that a decision is pending -- holding every call
	// would stall every session on the machine. A session is held only after
	// someone asked for it.
	armed map[string]armedSession
	// seq orders holds within a session; see heldPermission.seq.
	seq   atomic.Uint64
	nowFn func() time.Time
	// holdFor is forwarded to store before every hold: askgate.Store owns the
	// actual wait, but tests reach in and mutate this field directly, so it
	// must stay the field of record rather than a snapshot store captured once.
	holdFor  time.Duration
	noticeFn func() // called when the pending set changes, to nudge a rescan
	// deny reads the permission rules the user configured for Claude Code
	// itself. A held call those rules forbid is offered without an Allow.
	deny  *claudesettings.Reader
	store *askgate.Store[heldPermission]
}

// NewHookEnforcer builds a hook enforcer. onChange may be nil; when set it is
// called whenever the pending set changes so the caller can push a fresh
// snapshot to connected clients instead of waiting for the next scan tick.
func NewHookEnforcer(onChange func()) *HookEnforcer {
	b := &HookEnforcer{
		notices:  map[string]permissionNotice{},
		lapsed:   map[string]lapse{},
		armed:    map[string]armedSession{},
		nowFn:    time.Now,
		holdFor:  permissionHoldTimeout,
		noticeFn: onChange,
	}
	b.store = askgate.New[heldPermission](b.holdFor, b.changed)
	return b
}

// Point identifies this enforcement point.
//
// Unlike ServerEnforcer and SpawnEnforcer, this one fails OPEN on timeout, by
// design: hold lapses into writeNoDecision (see PermissionRequest), Claude
// Code falls back to drawing its own terminal prompt, and the session
// proceeds unblocked. That is the declared posture, not an oversight — the
// hold's budget (permissionHoldTimeout) is sized so this side gives up before
// Claude Code's own hook timeout, so a dashboard outage degrades a
// hand-started session to its normal prompt instead of hanging it forever.
// HookEnforcer does not read a capability's EnforceableBy at all — it holds
// every PreToolUse call that reaches it, regardless of what the capability
// declares. Whether a given tool call reaches this hook in the first place
// is decided by Claude Code's own hook wiring, not by this type.
//
// The rest of PermissionRequest also answers with no decision, but for two
// different reasons that must not be read as "fails closed" the way the
// timeout above does:
//   - An unarmed session (mayHoldLocked) or a malformed/incomplete payload is
//     never held at all. Nothing was evaluated, so there is nothing to fail
//     open or closed -- it is left entirely to Claude Code's own native
//     prompt, exactly as if the hook were not installed.
//   - A call the user's own deny rules forbid (deniedBy) IS actively vetoed:
//     it is held and offered without an Allow, and resolve refuses to turn it
//     into one even if asked (errDeniedByRule). That is the one guarantee
//     this type makes.
//
// Only a call that was genuinely held and got no answer in time lapses open
// on the timeout above.
func (b *HookEnforcer) Point() string { return capability.EnforcerHook }

// SetOnChange installs the callback fired whenever the pending set changes.
// The bridge is built in the DI container, before the debounced rescan the
// router owns exists, so the callback arrives afterwards rather than at
// construction.
func (b *HookEnforcer) SetOnChange(fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.noticeFn = fn
}

// SetDenyReader installs the source of the user's own permission rules. Until
// one is set the bridge offers every held call for approval, which is the
// behaviour a machine without a settings file has anyway.
func (b *HookEnforcer) SetDenyReader(r *claudesettings.Reader) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deny = r
}

func (b *HookEnforcer) changed() {
	b.mu.Lock()
	fn := b.noticeFn
	b.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// preToolPayload is the subset of Claude Code's PreToolUse payload the bridge uses.
type preToolPayload struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
	// CWD is the session's working directory. It is an identity check, not
	// decoration: see armedSession.
	CWD string `json:"cwd"`
}

// notificationPayload is the subset of Claude Code's Notification payload the
// bridge uses. NotificationType is a typed field rather than prose, which is
// what makes this usable as a signal at all.
type notificationPayload struct {
	SessionID        string `json:"session_id"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// permissionPromptNotification is the notification_type Claude Code sends when
// it draws its own permission prompt.
const permissionPromptNotification = "permission_prompt"

// Request handles POST /api/hooks/permission — the PreToolUse hook.
//
// It answers with the JSON the hook must print verbatim. An empty object means
// "no decision": the hook prints nothing and Claude Code proceeds with its
// normal flow, which is its terminal prompt. Every failure path answers that
// way, so a dashboard that is down, slow or unreachable degrades to exactly the
// behaviour a session has without the hook installed.
func (h *Handler) PermissionRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireSecret(w, r) {
		return
	}
	var body preToolPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBodyBytes)).Decode(&body); err != nil {
		writeNoDecision(w)
		return
	}
	if body.SessionID == "" || body.ToolName == "" {
		writeNoDecision(w)
		return
	}

	decision, ok := h.permissions.hold(r.Context(), body)
	if !ok {
		writeNoDecision(w)
		return
	}
	writeDecision(w, decision)
}

// PermissionNotify handles POST /api/hooks/notification — the Notification hook.
// It records that a session's own terminal prompt is up, which is the state the
// dashboard can report but not resolve.
func (h *Handler) PermissionNotify(w http.ResponseWriter, r *http.Request) {
	if !h.requireSecret(w, r) {
		return
	}
	var body notificationPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBodyBytes)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.NotificationType == permissionPromptNotification && body.SessionID != "" {
		h.permissions.noteTerminalPrompt(body.SessionID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// PermissionRespond handles POST /api/hooks/permission/respond — the browser.
//
// Authenticated by the session-auth group in router.go, not by the hooks bearer
// secret: this call carries the dashboard's session cookie, the same split the
// edit gate already uses.
func (h *Handler) PermissionRespond(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Decision string `json:"decision"` // "allow" | "deny"
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBodyBytes)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Decision != "allow" && body.Decision != "deny" {
		writeJSONError(w, http.StatusBadRequest, `decision must be "allow" or "deny"`)
		return
	}
	switch err := h.permissions.resolve(body.ID, body.Decision); {
	case errors.Is(err, errDeniedByRule):
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// PermissionArm handles POST /api/hooks/permission/arm — the browser.
//
// Arming is what makes the bridge hold anything at all. PreToolUse fires before
// Claude Code evaluates whether to prompt, so it cannot say "a decision is
// pending"; without an explicit opt-in the bridge would have to hold every tool
// call of every session to catch the few that matter.
//
// It is also where the bridge learns what a session IS. Arming resolves the
// session against the live scan and records the directory found there; a
// session the scanner does not know cannot be armed at all. See armedSession.
func (h *Handler) PermissionArm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
		Armed     bool   `json:"armed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBodyBytes)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.SessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "sessionId is required")
		return
	}
	var cwd string
	if body.Armed && h.sessionCWD != nil {
		found, ok := h.sessionCWD(r.Context(), body.SessionID)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "no live session with that id")
			return
		}
		cwd = found
	}
	h.permissions.Arm(body.SessionID, cwd, body.Armed)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"armed": body.Armed})
}

// hold registers the request and blocks until a decision arrives, the hold
// lapses, or the hook hangs up. The bool reports whether a decision was made.
func (b *HookEnforcer) hold(ctx context.Context, p preToolPayload) (string, bool) {
	seq := b.seq.Add(1)
	id := uuid.New().String()
	raw := argumentOf(p)
	pattern, patternElided := displayPattern(raw)
	deniedBy, deniedByElided := b.deniedBy(p, raw)
	meta := heldPermission{
		perm: sdk.PendingPermission{
			ID:             id,
			Tool:           p.ToolName,
			Pattern:        pattern,
			PatternElided:  patternElided,
			DeniedBy:       deniedBy,
			DeniedByElided: deniedByElided,
			Reason:         nil,
			RequestedAt:    b.nowFn().UTC().Format(time.RFC3339),
		},
		sessionID: p.SessionID,
		toolUseID: p.ToolUseID,
		seq:       seq,
	}

	b.mu.Lock()
	if !b.mayHoldLocked(p.SessionID, p.CWD) || b.holdsForLocked(p.SessionID) >= maxHoldsPerSession {
		b.mu.Unlock()
		return "", false
	}
	// A held call supersedes an older terminal notice for the same session: the
	// decision is ours again.
	delete(b.notices, p.SessionID)
	// The count check above and this registration must be atomic w.r.t. each
	// other, or concurrent holds for one session all read the same stale count
	// and all register past the cap — so both stay under b.mu, and the notice
	// delete lands together with the registration instead of a moment ahead of
	// it. Wait is called only after b.mu is released: it blocks, and must never
	// do so while holding a lock another goroutine needs.
	b.store.SetHoldFor(b.holdFor)
	holdFor, ch := b.store.Register(id, meta)
	b.mu.Unlock()
	b.changed()

	decision, ok := b.store.Wait(ctx, id, holdFor, ch)
	if !ok && ctx.Err() == nil {
		// Claude Code draws its own prompt for this exact call next; the
		// Notification that follows is attributed to it.
		b.noteLapse(p.SessionID, p.ToolUseID)
	}
	return decision, ok
}

func (b *HookEnforcer) noteLapse(sessionID, toolUseID string) {
	b.mu.Lock()
	b.lapsed[sessionID] = lapse{at: b.nowFn(), toolUseID: toolUseID}
	b.mu.Unlock()
}

// errNotPending means the request lapsed into the terminal prompt, or someone
// else already answered it.
var errNotPending = errors.New("this request is no longer waiting for a decision")

// errDeniedByRule means the call is covered by the user's own permissions.deny.
// The client hides Allow for such a request, but the rule has to hold here too:
// the client is not the gate, and a hook "allow" would short-circuit the very
// evaluation that would otherwise apply the rule.
var errDeniedByRule = errors.New("your own permission rules deny this call")

// resolve delivers a decision to a held call. The deniedBy veto and the
// store's own look-up/removal run as one critical section (see
// askgate.Store.Resolve): a hold that lapses in that same window must not
// see a decision delivered out from under it.
func (b *HookEnforcer) resolve(id, decision string) error {
	err := b.store.Resolve(id, func(meta heldPermission) (string, error) {
		if decision == "allow" && meta.perm.DeniedBy != nil {
			// Left pending on purpose: Deny is still a valid answer for it.
			return "", errDeniedByRule
		}
		return decision, nil
	})
	if errors.Is(err, askgate.ErrNotPending) {
		return errNotPending
	}
	return err
}

// deniedBy names the first configured rule forbidding this call, or nil.
//
// The RAW argument is matched, never the display copy: sanitizing collapses
// whitespace, so "rm  -rf /" would stop matching a `Bash(rm:*)` prefix that the
// session's own evaluation still applies.
func (b *HookEnforcer) deniedBy(p preToolPayload, raw string) (*string, int) {
	b.mu.Lock()
	reader := b.deny
	b.mu.Unlock()
	if reader == nil {
		return nil, 0
	}
	rule := claudesettings.FirstMatch(reader.DenyRules(p.CWD), p.ToolName, raw)
	if rule == nil {
		return nil, 0
	}
	// The rule text is the user's own, from their own settings file — but it is
	// rendered next to a decision, and the same display contract applies.
	shown, elided := sanitize.ForDisplayCapped(rule.Raw, maxPatternRunes)
	if shown == "" {
		return nil, 0
	}
	return &shown, elided
}

// Arm marks a session's prompts as ones the dashboard should intercept, or
// clears that mark. Arming is per session and expires: see armedTTL. cwd is the
// working directory the scanner reported for the session; see armedSession.
func (b *HookEnforcer) Arm(sessionID, cwd string, armed bool) {
	b.mu.Lock()
	if armed {
		b.armed[sessionID] = armedSession{at: b.nowFn(), cwd: cwd}
	} else {
		delete(b.armed, sessionID)
	}
	b.mu.Unlock()
	b.changed()
}

// mayHoldLocked reports whether a payload claiming this session may be held.
func (b *HookEnforcer) mayHoldLocked(sessionID, cwd string) bool {
	a, ok := b.armed[sessionID]
	if !ok {
		return false
	}
	if b.nowFn().Sub(a.at) > armedTTL {
		delete(b.armed, sessionID)
		return false
	}
	// Nothing to compare when either side reported no directory: a scan that
	// found none must not turn into a rejection of every call.
	return a.cwd == "" || cwd == "" || a.cwd == cwd
}

// holdsForLocked counts requests currently held for sessionID. It reads the
// store's own snapshot rather than a field of b, but the count is only valid
// as an admission check if the caller holds b.mu across it and whatever
// registration follows — see hold.
func (b *HookEnforcer) holdsForLocked(sessionID string) int {
	n := 0
	for _, e := range b.store.List() {
		if e.Meta.sessionID == sessionID {
			n++
		}
	}
	return n
}

func (b *HookEnforcer) noteTerminalPrompt(sessionID string) {
	b.mu.Lock()
	now := b.nowFn()
	notice := permissionNotice{at: now}
	if l, ok := b.lapsed[sessionID]; ok {
		delete(b.lapsed, sessionID)
		if now.Sub(l.at) <= lapseCorrelationWindow {
			notice.toolUseID = l.toolUseID
		}
	}
	b.notices[sessionID] = notice
	b.mu.Unlock()
	b.changed()
}

// PendingForSession returns the permission requests currently answerable from
// the dashboard for one session, plus whether that session's own terminal is
// showing a prompt nobody answered here.
// StateForSession is a pure read: it never mutates the bridge. Expiry is done
// by SweepExpired, called from the same tick that scans agents -- driving it
// from a getter made it observation-driven, so a session that raised a prompt
// and then exited kept its notice for the life of the process.
// Returns the requests answerable right now in arrival order, whether the
// session is showing its own prompt instead, and whether it is armed.
func (b *HookEnforcer) StateForSession(sessionID string) (held []sdk.PendingPermission, atTerminal bool, terminalToolUseID string, armed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	type ordered struct {
		perm sdk.PendingPermission
		seq  uint64
	}
	var hs []ordered
	for _, e := range b.store.List() {
		if e.Meta.sessionID == sessionID {
			hs = append(hs, ordered{perm: e.Meta.perm, seq: e.Meta.seq})
		}
	}
	// By arrival, not by RequestedAt: that has second precision, so a batch of
	// parallel tool calls would sort arbitrarily and the card could answer a
	// different request than the one it rendered.
	sort.Slice(hs, func(i, j int) bool { return hs[i].seq < hs[j].seq })
	out := make([]sdk.PendingPermission, len(hs))
	for i, h := range hs {
		out[i] = h.perm
	}

	notice, hasNotice := b.notices[sessionID]
	fresh := hasNotice && b.nowFn().Sub(notice.at) <= permissionNoticeTTL
	if !fresh {
		return out, false, "", b.isArmedReadLocked(sessionID)
	}
	return out, true, notice.toolUseID, b.isArmedReadLocked(sessionID)
}

// SweepExpired drops armed marks and terminal notices that have aged out. It is
// the only place either map shrinks on a timer, so it must be called
// periodically -- the agent scan tick does.
func (b *HookEnforcer) SweepExpired() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.nowFn()
	for id, a := range b.armed {
		if now.Sub(a.at) > armedTTL {
			delete(b.armed, id)
		}
	}
	for id, n := range b.notices {
		if now.Sub(n.at) > permissionNoticeTTL {
			delete(b.notices, id)
		}
	}
	// A lapse nothing followed up on explains nothing.
	for id, l := range b.lapsed {
		if now.Sub(l.at) > lapseCorrelationWindow {
			delete(b.lapsed, id)
		}
	}
}

// isArmedReadLocked is the non-mutating half of mayHoldLocked, for the read path.
func (b *HookEnforcer) isArmedReadLocked(sessionID string) bool {
	a, ok := b.armed[sessionID]
	return ok && b.nowFn().Sub(a.at) <= armedTTL
}

// argumentOf extracts the tool's own argument from the hook payload: the Bash
// command, the file path for a tool that names one, or the URL. This is the same
// value the parser derives from the transcript, but taken from the producer
// instead of reconstructed — so it is the argument the session is actually
// asking about.
//
// It is returned verbatim. Two consumers want opposite things from it: the deny
// matcher needs the exact bytes Claude Code will itself evaluate, and the card
// needs something safe to render. displayPattern is the second half.
func argumentOf(p preToolPayload) string {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		URL      string `json:"url"`
	}
	if json.Unmarshal(p.ToolInput, &in) != nil {
		return ""
	}
	if in.Command != "" {
		return in.Command
	}
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.URL
}

// displayPattern is the render-safe form of a tool argument.
//
// It is sanitized, unlike the parser's PendingToolUse.Pattern. That one is the
// grant identity, matched against a stored preset by exact equality, so
// normalising it would fold two distinct commands onto one rule. This one is
// display-only: the client posts {id, decision} and never sends the pattern
// anywhere. Leaving it raw put agent-authored text with a possible bidi override
// straight into the title of the Allow button — the same gap this project
// closed for the transcript path one layer up.
func displayPattern(raw string) (*string, int) {
	if raw == "" {
		return nil, 0
	}
	// Rune-capped by the sanitizer, not sliced by bytes: a cut through a
	// multi-byte character yields U+FFFD on the wire.
	display, elided := sanitize.ForDisplayCapped(raw, maxPatternRunes)
	if display == "" {
		return nil, 0
	}
	return &display, elided
}

func writeNoDecision(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	// An empty object, not an error status: the hook prints whatever it gets and
	// an empty object is Claude Code's "carry on as usual".
	_, _ = w.Write([]byte("{}\n"))
}

func writeDecision(w http.ResponseWriter, decision string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": "decided in the agent dashboard",
		},
	})
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
