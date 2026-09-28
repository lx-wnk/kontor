package provider

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/ollamaclient"
)

// OllamaClassifier decides whether a model is a locally-served (zero-cost)
// Ollama model. It caches the installed-model set from GET <base>/api/tags for
// a short TTL; an unreachable Ollama yields an empty set (by-name match fails,
// but an explicit provider=="ollama" still classifies local).
type OllamaClassifier struct {
	client *ollamaclient.Client

	mu      sync.Mutex
	tags    map[string]bool
	fetched time.Time
	ttl     time.Duration
}

func NewOllamaClassifier(base string) *OllamaClassifier {
	return &OllamaClassifier{
		client: ollamaclient.New(base, 800*time.Millisecond),
		ttl:    10 * time.Second,
	}
}

// IsLocal reports whether (provider, model) denotes a local zero-cost model.
func (o *OllamaClassifier) IsLocal(provider, model string) bool {
	if strings.EqualFold(provider, "ollama") {
		return true
	}
	name := normalizeModel(model)
	if name == "" {
		return false
	}
	return o.tagSet()[name]
}

// normalizeModel strips known local-provider prefixes and lowercases.
func normalizeModel(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	for _, p := range []string{"ollama_chat/", "ollama/"} {
		m = strings.TrimPrefix(m, p)
	}
	return m
}

func (o *OllamaClassifier) tagSet() map[string]bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tags != nil && time.Since(o.fetched) < o.ttl {
		return o.tags
	}
	o.tags = o.fetchTags()
	o.fetched = time.Now()
	return o.tags
}

func (o *OllamaClassifier) fetchTags() map[string]bool {
	set := map[string]bool{}
	names, err := o.client.Tags(context.Background())
	if err != nil {
		return set
	}
	for _, name := range names {
		set[strings.ToLower(name)] = true
	}
	return set
}
