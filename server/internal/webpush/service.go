// Package webpush provides VAPID key management and Web Push delivery.
package webpush

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	webpushlib "github.com/SherClockHolmes/webpush-go"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
)

const (
	keyVAPIDPublic  = "vapid_public_key"
	keyVAPIDPrivate = "vapid_private_key"
	keyVAPIDSubject = "vapid_subject"
)

// sendNotification is a package-level var so tests can override the real HTTP call.
var sendNotification = webpushlib.SendNotification

// Service manages VAPID keys and push subscription delivery.
type Service struct {
	cfgRepo rawrepo.NotificationConfigRepo
	subRepo rawrepo.PushSubscriptionRepo
}

// NewService returns a Service backed by the given repos.
func NewService(cfgRepo rawrepo.NotificationConfigRepo, subRepo rawrepo.PushSubscriptionRepo) *Service {
	return &Service{cfgRepo: cfgRepo, subRepo: subRepo}
}

// GenerateVAPIDKeys generates and persists VAPID keys if not already present.
// Idempotent — returns existing public key if already generated.
// Returns (publicKey, alreadyExisted, error).
func (s *Service) GenerateVAPIDKeys(ctx context.Context, subject string) (publicKey string, alreadyExisted bool, err error) {
	existing, found, err := s.cfgRepo.Get(ctx, keyVAPIDPublic)
	if err != nil {
		return "", false, fmt.Errorf("webpush.GenerateVAPIDKeys: check existing: %w", err)
	}
	if found {
		return existing, true, nil
	}

	if subject == "" {
		// PWA-09: VAPID subject must be a mailto: address or an https: URL.
		// To use https: form, pass e.g. "https://yourdomain.example.com" as the subject parameter.
		subject = "mailto:admin@localhost"
	}

	privKey, pubKey, err := webpushlib.GenerateVAPIDKeys()
	if err != nil {
		return "", false, fmt.Errorf("webpush.GenerateVAPIDKeys: generate: %w", err)
	}

	if err := s.cfgRepo.Set(ctx, keyVAPIDPrivate, privKey); err != nil {
		return "", false, fmt.Errorf("webpush.GenerateVAPIDKeys: persist private: %w", err)
	}
	if err := s.cfgRepo.Set(ctx, keyVAPIDPublic, pubKey); err != nil {
		return "", false, fmt.Errorf("webpush.GenerateVAPIDKeys: persist public: %w", err)
	}
	if err := s.cfgRepo.Set(ctx, keyVAPIDSubject, subject); err != nil {
		return "", false, fmt.Errorf("webpush.GenerateVAPIDKeys: persist subject: %w", err)
	}
	return pubKey, false, nil
}

// GetPublicKey returns the current public VAPID key, or ("", false, nil) if not yet generated.
func (s *Service) GetPublicKey(ctx context.Context) (publicKey string, found bool, err error) {
	pub, found, err := s.cfgRepo.Get(ctx, keyVAPIDPublic)
	if err != nil {
		return "", false, fmt.Errorf("webpush.GetPublicKey: %w", err)
	}
	return pub, found, nil
}

// RegisterSubscription stores a browser push subscription (idempotent via INSERT OR IGNORE on endpoint).
func (s *Service) RegisterSubscription(ctx context.Context, sub rawrepo.PushSubscription) error {
	if err := s.subRepo.Register(ctx, sub); err != nil {
		return fmt.Errorf("webpush.RegisterSubscription: %w", err)
	}
	return nil
}

// SendToAll sends a push notification payload to all registered subscribers.
// Continues on individual delivery errors (logs them). Returns aggregate error count.
func (s *Service) SendToAll(ctx context.Context, payload []byte) (int, error) {
	pubKey, found, err := s.cfgRepo.Get(ctx, keyVAPIDPublic)
	if err != nil {
		return 0, fmt.Errorf("webpush.SendToAll: load public key: %w", err)
	}
	if !found {
		return 0, fmt.Errorf("webpush.SendToAll: VAPID keys not generated")
	}
	privKey, _, err := s.cfgRepo.Get(ctx, keyVAPIDPrivate)
	if err != nil {
		return 0, fmt.Errorf("webpush.SendToAll: load private key: %w", err)
	}
	subject, _, err := s.cfgRepo.Get(ctx, keyVAPIDSubject)
	if err != nil {
		return 0, fmt.Errorf("webpush.SendToAll: load subject: %w", err)
	}
	if subject == "" {
		subject = "mailto:admin@localhost"
	}

	subs, err := s.subRepo.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("webpush.SendToAll: list subscriptions: %w", err)
	}

	errCount := 0
	for _, sub := range subs {
		resp, sendErr := sendNotification(payload, &webpushlib.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpushlib.Keys{
				P256dh: sub.P256dh,
				Auth:   sub.Auth,
			},
		}, &webpushlib.Options{
			VAPIDPublicKey:  pubKey,
			VAPIDPrivateKey: privKey,
			Subscriber:      subject,
			TTL:             30,
		})
		if sendErr != nil {
			if resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			slog.Warn("webpush: delivery failed", "endpoint", sub.Endpoint, "err", sendErr)
			errCount++
			continue
		}
		if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
			slog.Info("webpush: pruning stale subscription", "endpoint", sub.Endpoint, "status", resp.StatusCode)
			_ = s.subRepo.DeleteByEndpoint(ctx, sub.Endpoint)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return errCount, nil
}
