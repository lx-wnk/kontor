package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client is a simple REST client for the dashboard API.
type Client struct {
	host  string
	token string
	http  *http.Client
}

func newClient(cfg CLIConfig) *Client {
	return &Client{
		host:  strings.TrimRight(cfg.Host, "/"),
		token: cfg.Token,
		http:  &http.Client{},
	}
}

// get performs GET {path} and decodes the JSON response into out.
func (c *Client) get(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

// post performs POST {path} with jsonBody and decodes the JSON response into out.
func (c *Client) post(path string, body any, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

// put performs PUT {path} with jsonBody and decodes the JSON response into out.
func (c *Client) put(path string, body any, out any) error {
	return c.do(http.MethodPut, path, body, out)
}

func (c *Client) do(method, path string, reqBody, out any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.host+path, bodyReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// api.RequireSameOriginForMutations rejects a mutation without Origin; the CLI is its own server's client.
	req.Header.Set("Origin", c.host)
	resp, err := c.http.Do(req)
	if err != nil {
		// Provide a helpful message for connection refused.
		if strings.Contains(err.Error(), "connection refused") {
			return fmt.Errorf("connection refused — is the dashboard server running at %s?", c.host)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512] + "... (truncated)"
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "application/json") && ct != "" {
			return fmt.Errorf("HTTP %d (Content-Type: %s): %s", resp.StatusCode, ct, msg)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// stream opens an SSE connection and calls onEvent for each data line.
// Returns when ctx is cancelled or the server closes the stream.
func (c *Client) stream(ctx context.Context, path string, onEvent func([]byte)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			onEvent([]byte(data))
		}
	}
	return sc.Err()
}
