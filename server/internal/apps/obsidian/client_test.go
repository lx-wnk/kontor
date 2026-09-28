package obsidian_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
)

func vaultHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("note content"))
	}
}

// freshSelfSignedCert generates a new, unique self-signed certificate.
// httptest.NewTLSServer reuses ONE fixed built-in certificate across every
// server in the process, so two servers created that way are
// indistinguishable to fingerprint pinning — this generates a genuinely
// different certificate to prove the pin actually changes.
func freshSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "obsidian-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestInsecureLoopbackRefusedForPublicHost(t *testing.T) {
	_, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://example.com",
		APIKey:    "secret",
		VaultRoot: "notes",
		TLSMode:   obsidian.TLSInsecureLoopback,
	})
	if err == nil {
		t.Fatal("NewClient: want error constructing insecure-loopback against a public host, got nil")
	}
}

func TestPinnedRefusesAChangedFingerprint(t *testing.T) {
	ts1 := httptest.NewTLSServer(vaultHandler())
	addr := ts1.Listener.Addr().String()

	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://" + addr,
		APIKey:    "secret",
		VaultRoot: "notes",
		TLSMode:   obsidian.TLSPinned,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.Read(context.Background(), "notes/a.md"); err != nil {
		t.Fatalf("first Read (pins fingerprint): %v", err)
	}
	if client.PinnedFingerprint() == "" {
		t.Fatal("PinnedFingerprint: want non-empty after first connect")
	}
	ts1.Close()

	// Restart on the exact same address with a fresh, unrelated certificate —
	// on loopback this is usually a reinstall, but the client must not assume
	// that; the user confirms a changed certificate, the system does not.
	ts2 := httptest.NewUnstartedServer(vaultHandler())
	if err := ts2.Listener.Close(); err != nil {
		t.Fatalf("close default listener: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-listen on %s: %v", addr, err)
	}
	ts2.Listener = ln
	ts2.TLS = &tls.Config{Certificates: []tls.Certificate{freshSelfSignedCert(t)}}
	ts2.StartTLS()
	defer ts2.Close()

	if _, err := client.Read(context.Background(), "notes/a.md"); err == nil {
		t.Fatal("Read: want refusal after the certificate fingerprint changed")
	}
}

func TestVaultPathContainmentRefusesEscape(t *testing.T) {
	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://127.0.0.1:1", // never dialled — containment is checked first
		APIKey:    "secret",
		VaultRoot: "notes",
		TLSMode:   obsidian.TLSInsecureLoopback,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.Read(context.Background(), "../secrets/passwords.md"); err == nil {
		t.Fatal("Read: want refusal for a path escaping VaultRoot")
	}
	if err := client.Write(context.Background(), "../../etc/passwd", "pwned"); err == nil {
		t.Fatal("Write: want refusal for a path escaping VaultRoot")
	}
	if err := client.Delete(context.Background(), "../outside.md"); err == nil {
		t.Fatal("Delete: want refusal for a path escaping VaultRoot")
	}
}

// TestNormalizeNotePathCollapsesTraversal pins the exact reproduction from
// fix round 1: a raw agent-supplied path containing a ".." segment must
// resolve to the same canonical form the client itself would act on, not
// the raw string a capability check would otherwise see.
func TestNormalizeNotePathCollapsesTraversal(t *testing.T) {
	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://127.0.0.1:1", // never dialled
		APIKey:    "secret",
		VaultRoot: "root",
		TLSMode:   obsidian.TLSInsecureLoopback,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	got, err := client.NormalizeNotePath("notes/../secrets/keys.md")
	if err != nil {
		t.Fatalf("NormalizeNotePath: %v", err)
	}
	if got != "secrets/keys.md" {
		t.Fatalf("NormalizeNotePath(%q) = %q, want %q", "notes/../secrets/keys.md", got, "secrets/keys.md")
	}
}

func TestNormalizeNotePathRefusesEscape(t *testing.T) {
	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://127.0.0.1:1", // never dialled
		APIKey:    "secret",
		VaultRoot: "root",
		TLSMode:   obsidian.TLSInsecureLoopback,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.NormalizeNotePath("../outside.md"); err == nil {
		t.Fatal("NormalizeNotePath: want refusal for a path escaping VaultRoot")
	}
}

func TestAPIKeyNeverAppearsInAnError(t *testing.T) {
	const apiKey = "sekret-token-do-not-leak" //nolint:gosec // test fixture, not a real credential

	// A port nothing listens on: guaranteed connection failure with no
	// network dependency, forcing a request error without ever completing a
	// round trip.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://" + addr,
		APIKey:    apiKey,
		VaultRoot: "notes",
		TLSMode:   obsidian.TLSInsecureLoopback,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Read(context.Background(), "notes/a.md")
	if err == nil {
		t.Fatal("Read: want error dialling a closed port")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}

	if err := client.Write(context.Background(), "notes/a.md", "content"); err == nil {
		t.Fatal("Write: want error dialling a closed port")
	} else if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}

	if err := client.Delete(context.Background(), "notes/a.md"); err == nil {
		t.Fatal("Delete: want error dialling a closed port")
	} else if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}

	if _, err := client.Search(context.Background(), "query"); err == nil {
		t.Fatal("Search: want error dialling a closed port")
	} else if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestOpenNoteSendsTheRootResolvedPath(t *testing.T) {
	client, calls := newGraphVault(t, http.StatusOK)

	if err := client.OpenNote(context.Background(), "a.md"); err != nil {
		t.Fatalf("OpenNote: %v", err)
	}
	if want := []string{"POST /open/root/a.md"}; !slices.Equal(calls(), want) {
		t.Errorf("requests = %v, want %v", calls(), want)
	}
}

func TestOpenNoteRefusesAnEscapeBeforeAnyRequest(t *testing.T) {
	client, calls := newGraphVault(t, http.StatusOK)

	if err := client.OpenNote(context.Background(), "../etc.md"); err == nil {
		t.Fatal("OpenNote: want an error for a path escaping the vault root, got nil")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

func TestRootRelative(t *testing.T) {
	tests := []struct {
		root, vaultPath, want string
		ok                    bool
	}{
		{"claude-memory", "claude-memory/private/x.md", "private/x.md", true},
		{"claude-memory/", "claude-memory/x.md", "x.md", true},
		{"claude-memory", "claude-memory/a/../b.md", "b.md", true},
		{"claude-memory", "claude-memory/../other/x.md", "", false},
		{"claude-memory", "other/x.md", "", false},
		{"claude-memory", "claude-memory-old/x.md", "", false},
		{"claude-memory", "claude-memory", "", false},
		{"", "claude-memory/x.md", "", false},
		{"claude-memory", "", "", false},
	}
	for _, tt := range tests {
		got, ok := obsidian.RootRelative(tt.root, tt.vaultPath)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RootRelative(%q, %q) = (%q, %v), want (%q, %v)", tt.root, tt.vaultPath, got, ok, tt.want, tt.ok)
		}
	}
}
