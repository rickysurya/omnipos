package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Browsers cannot set an Authorization header on a WebSocket, so a kitchen display page offers
	// two subprotocols: wsSubprotocol, which the server selects, and wsTokenPrefix plus the token.
	// The token never appears in the URL, so it stays out of access logs.
	wsSubprotocol = "kds.v1"
	wsTokenPrefix = "kds-token."

	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = wsPongWait * 9 / 10
	wsMaxMessageSize = 1024 // a KDSAction is a few dozen bytes
	wsSendBuffer     = 64
	wsMaxClients     = 32

	kitchenUpdateTimeout = 5 * time.Second
)

// KitchenUpdater is the port the WebSocket adapter uses to apply kitchen status changes.
// Engine satisfies it, so this file never depends on domain internals.
type KitchenUpdater interface {
	ProcessKitchenUpdate(ctx context.Context, action KDSAction) error
}

// client is one connected kitchen display. Broadcasts only ever write to its buffered channel, so
// a stalled display cannot block the order flow.
type client struct {
	send      chan []byte
	closeConn func() error
}

type Hub struct {
	token          string
	allowedOrigins map[string]struct{}
	upgrader       websocket.Upgrader

	mu      sync.RWMutex
	clients map[*client]struct{}
	updater KitchenUpdater
}

func NewHub(token string, allowedOrigins []string) *Hub {
	h := &Hub{
		token:          token,
		allowedOrigins: make(map[string]struct{}, len(allowedOrigins)),
		clients:        make(map[*client]struct{}),
	}
	for _, origin := range allowedOrigins {
		h.allowedOrigins[strings.ToLower(origin)] = struct{}{}
	}
	h.upgrader = websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   1024,
		WriteBufferSize:  1024,
		Subprotocols:     []string{wsSubprotocol},
		CheckOrigin:      h.checkOrigin,
	}
	return h
}

func (h *Hub) SetKitchenUpdater(u KitchenUpdater) {
	h.updater = u
}

// presentedToken returns the credential from an Authorization bearer header or, for browsers,
// from the kds-token subprotocol.
func presentedToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, protocol := range strings.Split(header, ",") {
			protocol = strings.TrimSpace(protocol)
			if strings.HasPrefix(protocol, wsTokenPrefix) {
				return strings.TrimPrefix(protocol, wsTokenPrefix)
			}
		}
	}
	return ""
}

func (h *Hub) authorized(r *http.Request) bool {
	return secureEqual(presentedToken(r), h.token)
}

// checkOrigin lets clients without an Origin header through (they are not browsers, and the token
// still applies) and requires every browser origin to be on the allowlist.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	_, ok := h.allowedOrigins[strings.ToLower(origin)]
	return ok
}

func (h *Hub) add(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= wsMaxClients {
		return false
	}
	h.clients[c] = struct{}{}
	return true
}

// remove is safe to call more than once and from any goroutine.
func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
	_ = c.closeConn()
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast never blocks. A client whose buffer is full is too slow to keep up and is dropped;
// it can reconnect.
func (h *Hub) Broadcast(msg string) {
	data := []byte(msg)
	var slow []*client

	h.mu.RLock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			slow = append(slow, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range slow {
		h.remove(c)
	}
}

// Close disconnects every kitchen display. It runs when the HTTP server shuts down.
func (h *Hub) Close() {
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()

	for _, c := range clients {
		_ = c.closeConn()
	}
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		log.Printf("ws: rejected unauthenticated connection from %s", r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.checkOrigin(r) {
		log.Printf("ws: rejected origin %q from %s", r.Header.Get("Origin"), r.RemoteAddr)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if h.clientCount() >= wsMaxClients {
		http.Error(w, "Too many kitchen displays connected", http.StatusServiceUnavailable)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}

	c := &client{send: make(chan []byte, wsSendBuffer), closeConn: conn.Close}
	if !h.add(c) {
		_ = conn.Close()
		return
	}

	go h.writePump(c, conn)
	h.readPump(c, conn)
}

// readPump is the only reader of conn. It applies a size limit and a liveness deadline that every
// pong extends, then hands well formed kitchen updates to the domain.
func (h *Hub) readPump(c *client, conn *websocket.Conn) {
	defer h.remove(c)

	conn.SetReadLimit(wsMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var action KDSAction
		if err := json.Unmarshal(message, &action); err != nil || action.OrderID <= 0 || h.updater == nil {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), kitchenUpdateTimeout)
		err = h.updater.ProcessKitchenUpdate(ctx, action)
		cancel()
		if err != nil {
			log.Printf("ws: kitchen update for order %d rejected: %v", action.OrderID, err)
		}
	}
}

// writePump is the only writer of conn, which is what gorilla/websocket requires.
func (h *Hub) writePump(c *client, conn *websocket.Conn) {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		h.remove(c)
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
