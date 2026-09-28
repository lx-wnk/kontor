package memory_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/memory"
)

func TestSanitizeForStoreRedactsSecrets(t *testing.T) {
	_, content, err := memory.SanitizeForStore("summary", "the token is sk-ant-api03-REDACTMEREDACTMEREDACTMEREDACTME")
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if strings.Contains(content, "sk-ant-api03-REDACTMEREDACTMEREDACTMEREDACTME") {
		t.Error("a secret must not reach the store — it is persistent, and later scrubbing cannot help")
	}
}

func TestSanitizeForStoreStripsBidiControls(t *testing.T) {
	// Escaped, not written literally: a raw U+202E in this file would reverse
	// the rendering of the source line that documents it, which is the very
	// attack the function under test defends against.
	_, content, err := memory.SanitizeForStore("summary", "safe\u202etxet desrever\u202c")
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if strings.ContainsRune(content, '‮') {
		t.Error("bidi override must be stripped — this text is rendered and concatenated into prompts")
	}
}

func TestSanitizeForStoreRefusesWhenEmptied(t *testing.T) {
	_, _, err := memory.SanitizeForStore("", "\u202e\u202c")
	if !errors.Is(err, memory.ErrEmptyAfterSanitize) {
		t.Fatalf("err = %v, want ErrEmptyAfterSanitize — a silently emptied entry is worse than a rejected one", err)
	}
}

func TestSanitizeForStoreKeepsTextAdjacentToSecret(t *testing.T) {
	_, content, err := memory.SanitizeForStore("summary",
		"please rotate this key: sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA immediately")
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if strings.Contains(content, "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Error("secret must be redacted")
	}
	if !strings.Contains(content, "please rotate this key:") || !strings.Contains(content, "immediately") {
		t.Errorf("text adjacent to the secret must survive, got %q", content)
	}
}

func TestSanitizeForStoreRedactsSecretSpanningLineBreak(t *testing.T) {
	content := "-----BEGIN RSA PRIVATE KEY-----\nMIIBOwIBAAJBAKEXAMPLEKEYMATERIAL\n-----END RSA PRIVATE KEY-----"
	_, got, err := memory.SanitizeForStore("summary", content)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if strings.Contains(got, "MIIBOwIBAAJBAKEXAMPLEKEYMATERIAL") {
		t.Error("a secret spanning a line break must still be redacted")
	}
}

func TestSanitizeForStoreEntirelySecretStillNonEmpty(t *testing.T) {
	_, content, err := memory.SanitizeForStore("summary", "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if content == "" {
		t.Error("a redacted placeholder is not an empty entry — it must not be refused")
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Errorf("expected a redaction marker, got %q", content)
	}
}

func TestSanitizeForStoreContentKeepsLineStructure(t *testing.T) {
	multiline := "step 1: do this\nstep 2: do that\n\nfunc example() {\n\treturn nil\n}"
	_, content, err := memory.SanitizeForStore("summary", multiline)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if content != multiline {
		t.Errorf("content lost its line structure: got %q, want %q", content, multiline)
	}
}

func TestSanitizeForStoreSummaryStaysSingleLine(t *testing.T) {
	summary, _, err := memory.SanitizeForStore("line one\nline two", "content")
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if strings.Contains(summary, "\n") {
		t.Errorf("summary must stay single-line — it cannot forge a prompt section boundary, got %q", summary)
	}
}
