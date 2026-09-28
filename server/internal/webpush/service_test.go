package webpush

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	webpushlib "github.com/SherClockHolmes/webpush-go"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
)

func newTestService(t *testing.T) (*Service, rawrepo.PushSubscriptionRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	cfgRepo := rawrepo.NewNotificationConfigRepo(bundle.DB)
	subRepo := rawrepo.NewPushSubscriptionRepo(bundle.DB)
	return NewService(cfgRepo, subRepo), subRepo
}

// TestSendToAll_PrunesGoneSubscription verifies that a 410 response causes the
// subscription to be deleted while subscriptions with 201 responses are retained.
func TestSendToAll_PrunesGoneSubscription(t *testing.T) {
	ctx := context.Background()
	svc, subRepo := newTestService(t)

	if _, _, err := svc.GenerateVAPIDKeys(ctx, "mailto:test@example.com"); err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}

	endpointGone := "https://push.example/gone"
	endpointOK := "https://push.example/ok"

	if err := subRepo.Register(ctx, rawrepo.PushSubscription{
		Endpoint: endpointGone, P256dh: "key1", Auth: "auth1",
	}); err != nil {
		t.Fatalf("Register gone: %v", err)
	}
	if err := subRepo.Register(ctx, rawrepo.PushSubscription{
		Endpoint: endpointOK, P256dh: "key2", Auth: "auth2",
	}); err != nil {
		t.Fatalf("Register ok: %v", err)
	}

	orig := sendNotification
	t.Cleanup(func() { sendNotification = orig })
	sendNotification = func(payload []byte, sub *webpushlib.Subscription, opts *webpushlib.Options) (*http.Response, error) {
		code := http.StatusCreated
		if sub.Endpoint == endpointGone {
			code = http.StatusGone
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	if _, err := svc.SendToAll(ctx, []byte(`{"title":"hi"}`)); err != nil {
		t.Fatalf("SendToAll: %v", err)
	}

	remaining, err := subRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 subscription after pruning, got %d", len(remaining))
	}
	if remaining[0].Endpoint != endpointOK {
		t.Errorf("expected remaining endpoint %q, got %q", endpointOK, remaining[0].Endpoint)
	}
}

// TestSendToAll_NoVAPIDKeys verifies that SendToAll returns an error when no
// VAPID keys have been generated yet.
func TestSendToAll_NoVAPIDKeys(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	_, err := svc.SendToAll(ctx, []byte(`{"title":"hi"}`))
	if err == nil {
		t.Fatal("expected error when VAPID keys absent, got nil")
	}
	if !strings.Contains(err.Error(), "VAPID") {
		t.Errorf("expected error to mention VAPID, got: %v", err)
	}
}
