package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testKDSToken = "kds-token-for-tests-0123456789abcdef"

func newTestClient(buffer int) (*client, *int) {
	closed := 0
	c := &client{
		send:      make(chan []byte, buffer),
		closeConn: func() error { closed++; return nil },
	}
	return c, &closed
}

func TestPresentedToken(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"bearer header", http.Header{"Authorization": {"Bearer abc123"}}, "abc123"},
		{"wrong scheme", http.Header{"Authorization": {"Basic abc123"}}, ""},
		{"subprotocol pair", http.Header{"Sec-Websocket-Protocol": {"kds.v1, kds-token.abc123"}}, "abc123"},
		{"subprotocol split over two headers", http.Header{"Sec-Websocket-Protocol": {"kds.v1", "kds-token.abc123"}}, "abc123"},
		{"subprotocol without a token", http.Header{"Sec-Websocket-Protocol": {"kds.v1"}}, ""},
		{"nothing", http.Header{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Header = c.headers
			if got := presentedToken(req); got != c.want {
				t.Fatalf("presentedToken = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCheckOrigin(t *testing.T) {
	hub := NewHub(testKDSToken, []string{"https://kds.example.com"})
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true}, // not a browser; the token still applies
		{"https://kds.example.com", true},
		{"HTTPS://KDS.EXAMPLE.COM", true},
		{"https://evil.example.com", false},
		{"http://kds.example.com", false},
		{"null", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if got := hub.checkOrigin(req); got != c.want {
			t.Errorf("checkOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}

	noOrigins := NewHub(testKDSToken, nil)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://kds.example.com")
	if noOrigins.checkOrigin(req) {
		t.Error("with no allowlist configured every browser origin must be rejected")
	}
}

func TestHandleWebSocketRejectsBeforeUpgrade(t *testing.T) {
	hub := NewHub(testKDSToken, []string{"https://kds.example.com"})
	cases := []struct {
		name    string
		headers http.Header
		want    int
	}{
		{"no credentials", http.Header{}, http.StatusUnauthorized},
		{"wrong token", http.Header{"Authorization": {"Bearer not-the-token"}}, http.StatusUnauthorized},
		{"token in the query string is ignored", http.Header{}, http.StatusUnauthorized},
		{"right token, foreign origin", http.Header{"Authorization": {"Bearer " + testKDSToken}, "Origin": {"https://evil.example.com"}}, http.StatusForbidden},
		{"right token by subprotocol, foreign origin", http.Header{"Sec-Websocket-Protocol": {"kds.v1, kds-token." + testKDSToken}, "Origin": {"https://evil.example.com"}}, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := "/ws"
			if c.name == "token in the query string is ignored" {
				target = "/ws?token=" + testKDSToken
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header = c.headers
			rec := httptest.NewRecorder()
			hub.HandleWebSocket(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestHubWithoutTokenRejectsEveryone(t *testing.T) {
	hub := NewHub("", nil)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	hub.HandleWebSocket(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unconfigured token must never authorize, got %d", rec.Code)
	}
}

func TestBroadcastDropsSlowClientsWithoutBlocking(t *testing.T) {
	hub := NewHub(testKDSToken, nil)
	fast, fastClosed := newTestClient(10)
	slow, slowClosed := newTestClient(1)
	hub.add(fast)
	hub.add(slow)

	done := make(chan struct{})
	go func() {
		hub.Broadcast("one")
		hub.Broadcast("two") // slow has no reader and a full buffer by now
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a stalled client")
	}

	if hub.clientCount() != 1 {
		t.Fatalf("client count = %d, want 1 (only the fast client)", hub.clientCount())
	}
	if *slowClosed != 1 {
		t.Fatalf("slow client connection closed %d times, want 1", *slowClosed)
	}
	if *fastClosed != 0 {
		t.Fatal("the healthy client must stay connected")
	}
	if got := len(fast.send); got != 2 {
		t.Fatalf("healthy client has %d queued messages, want 2", got)
	}
	if string(<-fast.send) != "one" || string(<-fast.send) != "two" {
		t.Fatal("messages must arrive in order")
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	hub := NewHub(testKDSToken, nil)
	c, _ := newTestClient(1)
	hub.add(c)
	hub.remove(c)
	hub.remove(c) // a second close of the channel would panic
	if hub.clientCount() != 0 {
		t.Fatalf("client count = %d, want 0", hub.clientCount())
	}
}

func TestClientCap(t *testing.T) {
	hub := NewHub(testKDSToken, nil)
	for i := 0; i < wsMaxClients; i++ {
		c, _ := newTestClient(1)
		if !hub.add(c) {
			t.Fatalf("client %d should have been accepted", i+1)
		}
	}
	extra, _ := newTestClient(1)
	if hub.add(extra) {
		t.Fatal("a client beyond the cap must be refused")
	}
}

func TestCloseDisconnectsEveryone(t *testing.T) {
	hub := NewHub(testKDSToken, nil)
	a, aClosed := newTestClient(1)
	b, bClosed := newTestClient(1)
	hub.add(a)
	hub.add(b)

	hub.Close()

	if hub.clientCount() != 0 || *aClosed != 1 || *bClosed != 1 {
		t.Fatalf("after Close: clients=%d, closed a=%d b=%d", hub.clientCount(), *aClosed, *bClosed)
	}
	hub.remove(a) // the read and write pumps still call this on their way out
}
