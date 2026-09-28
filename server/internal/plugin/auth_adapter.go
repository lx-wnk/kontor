package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/lx-wnk/kontor/server/internal/auth"
)

// PluginAuthProvider implements auth.OAuthProvider by proxying to an auth_provider plugin.
type PluginAuthProvider struct {
	entry  Entry
	client *http.Client
}

// NewAuthProvider wraps an auth_provider Entry as an auth.OAuthProvider.
func NewAuthProvider(e Entry) auth.OAuthProvider {
	return &PluginAuthProvider{
		entry:  e,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *PluginAuthProvider) BuildAuthURL(ctx context.Context, state, redirectURI string) (string, error) {
	u := p.entry.BaseURL + "/capabilities/auth/authorize-url" +
		"?state=" + url.QueryEscape(state) + "&redirect_uri=" + url.QueryEscape(redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("plugin BuildAuthURL: create request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("plugin BuildAuthURL: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plugin BuildAuthURL: non-200 status %d", resp.StatusCode)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("plugin BuildAuthURL: decode failed: %w", err)
	}
	return result.URL, nil
}

func (p *PluginAuthProvider) ExchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	type exchangeReq struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	body, err := json.Marshal(exchangeReq{Code: code, RedirectURI: redirectURI})
	if err != nil {
		return "", fmt.Errorf("plugin ExchangeCode: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.entry.BaseURL+"/capabilities/auth/exchange", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("plugin ExchangeCode: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("plugin ExchangeCode: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plugin ExchangeCode: non-200 status %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("plugin ExchangeCode: decode: %w", err)
	}
	return out.Token, nil
}

func (p *PluginAuthProvider) GetUser(ctx context.Context, accessToken string) (*auth.OAuthUserProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.entry.BaseURL+"/capabilities/auth/user", nil)
	if err != nil {
		return nil, fmt.Errorf("plugin GetUser: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plugin GetUser: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin GetUser: non-200 status %d", resp.StatusCode)
	}
	var profile auth.OAuthUserProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("plugin GetUser: decode: %w", err)
	}
	return &profile, nil
}
