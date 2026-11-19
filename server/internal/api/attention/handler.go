package attention

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const maxBodyBytes = 4096

var keyPattern = regexp.MustCompile(`^[a-z]+:[A-Za-z0-9._-]{1,128}$`)

// Handler serves the attention marks API and its SSE stream.
type Handler struct {
	marks       repo.AttentionMarkRepo
	broadcaster *sse.TypedBroadcaster[sse.AttentionEvent]
}

// NewHandler panics on nil arguments.
func NewHandler(marks repo.AttentionMarkRepo, b *sse.TypedBroadcaster[sse.AttentionEvent]) *Handler {
	if marks == nil || b == nil {
		panic("attention.NewHandler: nil dependency")
	}
	return &Handler{marks: marks, broadcaster: b}
}

// Mount registers the attention routes on r. Caller must wrap r with RequireAuth.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/attention/marks", apierr.ErrorMiddleware(h.List))
	r.Put("/api/attention/marks", apierr.ErrorMiddleware(h.Put))
	r.Get("/api/attention/stream", h.Stream)
}

type markView struct {
	Key         string `json:"key"`
	SeenThrough string `json:"seenThrough"`
}

func newMarkView(m *ent.AttentionMark) markView {
	return markView{Key: m.ItemKey, SeenThrough: m.SeenThrough.UTC().Format(time.RFC3339Nano)}
}

type listResponse struct {
	Baseline string     `json:"baseline"`
	Marks    []markView `json:"marks"`
}

// List returns the baseline and all marks.
// GET /api/attention/marks
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	baseline, err := h.marks.Baseline(r.Context())
	if err != nil {
		return err
	}
	rows, err := h.marks.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]markView, len(rows))
	for i, m := range rows {
		out[i] = newMarkView(m)
	}
	apierr.WriteJSON(w, http.StatusOK, listResponse{
		Baseline: baseline.UTC().Format(time.RFC3339Nano),
		Marks:    out,
	})
	return nil
}

type putBody struct {
	Key         string `json:"key"`
	SeenThrough string `json:"seenThrough"`
}

// Put raises the mark for an item and broadcasts when it changed.
// PUT /api/attention/marks
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body putBody
	if err := dec.Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if !keyPattern.MatchString(body.Key) {
		return apierr.NewAppError(http.StatusBadRequest, "key must look like kind:id")
	}
	seen, err := time.Parse(time.RFC3339Nano, body.SeenThrough)
	if err != nil || seen.IsZero() {
		return apierr.NewAppError(http.StatusBadRequest, "seenThrough must be a non-zero RFC 3339 timestamp")
	}
	mark, changed, err := h.marks.Mark(r.Context(), body.Key, seen)
	if err != nil {
		return err
	}
	view := newMarkView(mark)
	if changed {
		h.broadcaster.Broadcast(sse.AttentionEvent{Type: sse.AttentionMarked, Mark: view})
	}
	apierr.WriteJSON(w, http.StatusOK, view)
	return nil
}

// Stream serves GET /api/attention/stream — live attention mark events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)
	sse.WriteHeaders(w)
	flusher.Flush()
	for {
		select {
		case data, ok := <-sub:
			if !ok {
				return
			}
			w.Write(data) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
