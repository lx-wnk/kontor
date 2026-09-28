package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/sdk"
)

// fakeBroker stands in for the pty-broker's /ws endpoint: it sends a hello
// frame on connect, then echoes every subsequent frame it receives. The
// incoming Authorization header is published on the returned buffered channel
// so tests can assert the server-side bearer token was attached without racing
// on a shared variable.
func fakeBroker(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	auth := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "")
		ctx := r.Context()

		if err := c.Write(ctx, websocket.MessageBinary, []byte("HELLO")); err != nil {
			return
		}
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if err := c.Write(ctx, typ, data); err != nil {
				return
			}
		}
	})
	return httptest.NewServer(mux), auth
}

func brokerPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	u = strings.TrimPrefix(u, "http://localhost:")
	port, err := strconv.Atoi(u)
	require.NoError(t, err)
	return port
}

func mountTerminalHandler(h *TerminalHandler) *httptest.Server {
	r := chi.NewRouter()
	r.Get("/api/agents/{pid}/terminal", h.Terminal)
	return httptest.NewServer(r)
}

func TestTerminalProxy_BridgesFramesBothWays(t *testing.T) {
	broker, auth := fakeBroker(t)
	defer broker.Close()
	port := brokerPort(t, broker)

	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4242, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return port, "tok", nil }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4242/terminal"
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.Close(websocket.StatusNormalClosure, "")

	// The server must attach the broker token — and only that token — to its own
	// broker dial; the browser never supplied credentials of its own.
	select {
	case got := <-auth:
		require.Equal(t, "Bearer tok", got)
	case <-ctx.Done():
		t.Fatal("broker never received a request")
	}

	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "HELLO", string(data))

	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("x")))
	typ, data, err = c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "x", string(data))
}

func TestTerminalProxy_NoTerminal_Returns409(t *testing.T) {
	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4243, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return 0, "", ErrNoTerminal }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4243/terminal"
	_, resp, err := websocket.Dial(ctx, url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestTerminalProxy_AgentNotInjectable_Returns409(t *testing.T) {
	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4244, LiveInjectable: false}}, nil
	}
	target := func(pid int) (int, string, error) {
		t.Fatalf("target should not be called for a non-injectable agent")
		return 0, "", nil
	}

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4244/terminal"
	_, resp, err := websocket.Dial(ctx, url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestTerminalProxy_UnknownAgent_Returns409(t *testing.T) {
	getAgents := func(context.Context) ([]sdk.Agent, error) { return nil, nil }
	target := func(pid int) (int, string, error) {
		t.Fatalf("target should not be called for an unknown agent")
		return 0, "", nil
	}

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/9999/terminal"
	_, resp, err := websocket.Dial(ctx, url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// TestTerminalProxy_CrossOriginRejected simulates the CSWSH attack: a browser
// tab on an attacker-controlled origin opens a WebSocket straight at this
// route. WebSocket upgrades are exempt from CORS, so without the same-origin
// check this would succeed and hand the attacker a live pty. It must be
// rejected before the handler ever dials the broker.
func TestTerminalProxy_CrossOriginRejected(t *testing.T) {
	broker, auth := fakeBroker(t)
	defer broker.Close()
	port := brokerPort(t, broker)

	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4245, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return port, "tok", nil }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4245/terminal"
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://evil.example"}},
	})
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	select {
	case <-auth:
		t.Fatal("broker must not be dialed when the browser origin is rejected")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestTerminalProxy_SameOriginAccepted mirrors a real browser tab served from
// this app's own origin: Origin equals the request Host, so the handshake
// must succeed and bridge frames as before.
func TestTerminalProxy_SameOriginAccepted(t *testing.T) {
	broker, auth := fakeBroker(t)
	defer broker.Close()
	port := brokerPort(t, broker)

	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4246, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return port, "tok", nil }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4246/terminal"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {srv.URL}},
	})
	require.NoError(t, err)
	defer c.Close(websocket.StatusNormalClosure, "")

	select {
	case got := <-auth:
		require.Equal(t, "Bearer tok", got)
	case <-ctx.Done():
		t.Fatal("broker never received a request")
	}

	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "HELLO", string(data))
}

// TestTerminalProxy_NoOriginAccepted covers non-browser clients (CLI tools,
// server-to-server callers) that never send an Origin header at all — there
// is no browser to defend against, so these must still be allowed through.
func TestTerminalProxy_NoOriginAccepted(t *testing.T) {
	broker, auth := fakeBroker(t)
	defer broker.Close()
	port := brokerPort(t, broker)

	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4247, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return port, "tok", nil }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4247/terminal"
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.Close(websocket.StatusNormalClosure, "")

	select {
	case got := <-auth:
		require.Equal(t, "Bearer tok", got)
	case <-ctx.Done():
		t.Fatal("broker never received a request")
	}
}

// largeFrameBroker sends a single binary frame of the given size on connect.
// It exercises the pty broker's scrollback replay, which is emitted as one
// frame up to 256 KiB — well past coder/websocket's 32 KiB default read limit.
func largeFrameBroker(t *testing.T, size int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "")
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte('a' + i%26)
		}
		_ = c.Write(r.Context(), websocket.MessageBinary, payload)
	})
	return httptest.NewServer(mux)
}

// A scrollback replay larger than the 32 KiB default read limit must survive
// the proxy hop. Without lifting the read limit, the proxy's broker-side read
// aborts with ErrMessageTooBig and tears the terminal connection down.
func TestTerminalProxy_LargeReplayFrameSurvives(t *testing.T) {
	const size = 64 * 1024
	broker := largeFrameBroker(t, size)
	defer broker.Close()
	port := brokerPort(t, broker)

	getAgents := func(context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{PID: 4242, LiveInjectable: true}}, nil
	}
	target := func(pid int) (int, string, error) { return port, "tok", nil }

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/4242/terminal"
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.Close(websocket.StatusNormalClosure, "")
	// The real browser client (JS WebSocket) has no such limit; lift it here so
	// the test asserts the proxy relayed the whole frame, not the client cap.
	c.SetReadLimit(-1)

	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Len(t, data, size)
}

func TestTerminalProxy_InvalidPID_Returns400(t *testing.T) {
	getAgents := func(context.Context) ([]sdk.Agent, error) { return nil, nil }
	target := func(pid int) (int, string, error) {
		t.Fatalf("target should not be called for an invalid pid")
		return 0, "", nil
	}

	h := NewTerminalHandler(getAgents, target)
	srv := mountTerminalHandler(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agents/not-a-pid/terminal"
	_, resp, err := websocket.Dial(ctx, url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
