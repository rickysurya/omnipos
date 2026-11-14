package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sentMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// newFakeTelegram stands in for api.telegram.org and records every sendMessage call.
func newFakeTelegram(t *testing.T, token string) (*httptest.Server, chan sentMessage) {
	t.Helper()
	got := make(chan sentMessage, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bot"+token+"/sendMessage" {
			http.NotFound(w, r)
			return
		}
		var m sentMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		got <- m
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func waitForMessage(t *testing.T, ch <-chan sentMessage) sentMessage {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a Telegram message")
		return sentMessage{}
	}
}

func TestTelegramNotifierSendsMessage(t *testing.T) {
	srv, got := newFakeTelegram(t, "TOKEN")
	n := NewTelegramNotifier("TOKEN")
	n.baseURL = srv.URL

	if err := n.SendNotification("12345", "hello"); err != nil {
		t.Fatalf("SendNotification returned error: %v", err)
	}

	m := waitForMessage(t, got)
	if m.ChatID != 12345 || m.Text != "hello" {
		t.Fatalf("unexpected message: %+v", m)
	}
}

func TestTelegramNotifierReportsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"description":"chat not found"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	n := NewTelegramNotifier("TOKEN")
	n.baseURL = srv.URL

	err := n.SendNotification("1", "x")
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("error should carry status and description, got: %v", err)
	}
}

func TestTelegramNotifierErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // connection refused from here on

	n := NewTelegramNotifier("SECRET-TOKEN")
	n.baseURL = srv.URL

	err := n.SendNotification("1", "x")
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("error leaks the bot token: %v", err)
	}
}

func TestTelegramAuthMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	cases := []struct {
		name   string
		secret string
		header string
		want   int
	}{
		{"matching secret", "s3cret", "s3cret", http.StatusNoContent},
		{"wrong secret", "s3cret", "nope", http.StatusUnauthorized},
		{"missing header", "s3cret", "", http.StatusUnauthorized},
		{"unset secret never authorizes", "", "", http.StatusUnauthorized},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
			if c.header != "" {
				req.Header.Set("X-Telegram-Bot-Api-Secret-Token", c.header)
			}
			rec := httptest.NewRecorder()
			telegramAuthMiddleware(c.secret)(next).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("got status %d, want %d", rec.Code, c.want)
			}
		})
	}
}

const legacySchema = `
CREATE TABLE customers (
    chat_id VARCHAR(50) PRIMARY KEY,
    current_state VARCHAR(50) NOT NULL DEFAULT 'idle'
);
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    customer_id VARCHAR(50) REFERENCES customers(chat_id),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    address TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT orders_status_check CHECK (status IN ('pending', 'completed', 'cancelled'))
);
CREATE TABLE order_items (
    id SERIAL PRIMARY KEY,
    order_id INT REFERENCES orders(id) ON DELETE CASCADE,
    item_name VARCHAR(100) NOT NULL,
    quantity INT NOT NULL DEFAULT 1
);
`

// legacyRows are the awkward leftovers the migrations must cope with: an order line from before
// prices existed, and a customer who somehow has two open carts.
const legacyRows = `
INSERT INTO customers (chat_id) VALUES ('legacy');
INSERT INTO orders (customer_id, status) VALUES ('legacy', 'pending'), ('legacy', 'pending');
INSERT INTO order_items (order_id, item_name, quantity) VALUES (1, 'Old free text item', 3);
`

// TestKDSEndToEnd takes an old database through every migration, then drives a full order through
// the Telegram webhook, plays the kitchen over the WebSocket, and checks the database and the
// outbound Telegram messages at every step. It needs a PostgreSQL server and a role that may
// create tables, which means the owner role and not the application role:
//
//	TEST_DATABASE_URL=postgres://omnipos_owner:<password>@localhost:5432/omnipos go test ./...
//
// All tables live in a throwaway schema that is dropped afterwards, so existing data in the target
// database is never touched.
func TestKDSEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	schema := fmt.Sprintf("e2e_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	// An old database goes through every migration file, in order, each run twice.
	if _, err := pool.Exec(ctx, legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if _, err := pool.Exec(ctx, legacyRows); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (customer_id, status) VALUES ('legacy', 'preparing')`); err == nil {
		t.Fatal("the legacy constraint should reject status 'preparing'")
	}

	files, err := filepath.Glob("migrations/*.sql")
	if err != nil || len(files) < 2 {
		t.Fatalf("expected at least two migration files, got %v (err %v)", files, err)
	}
	sort.Strings(files)
	for _, f := range files {
		sqlText, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for run := 1; run <= 2; run++ {
			if _, err := pool.Exec(ctx, string(sqlText)); err != nil {
				t.Fatalf("%s, run %d: %v", f, run, err)
			}
		}
	}

	for _, status := range []string{"confirmed", "preparing", "ready"} {
		if _, err := pool.Exec(ctx, `INSERT INTO orders (customer_id, status) VALUES ('legacy', $1)`, status); err != nil {
			t.Fatalf("status %q should be accepted after the migrations: %v", status, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (customer_id, status) VALUES ('legacy', 'bogus')`); err == nil {
		t.Fatal("unknown statuses must still be rejected")
	}
	if got := count(`SELECT count(*) FROM orders WHERE customer_id = 'legacy' AND id = 1 AND status = 'cancelled'`); got != 1 {
		t.Fatal("the older of two open carts should have been cancelled by the migration")
	}
	if got := count(`SELECT count(*) FROM orders WHERE customer_id = 'legacy' AND id = 2 AND status = 'pending'`); got != 1 {
		t.Fatal("the newer open cart should have been kept")
	}
	if got := count(`SELECT count(*) FROM order_items WHERE item_name = 'Old free text item' AND unit_price_cents = 0 AND menu_item_id IS NULL`); got != 1 {
		t.Fatal("an old order line should survive with a price of 0 and no menu item")
	}

	// The database refuses a second open cart for the same customer.
	if _, err := pool.Exec(ctx, `INSERT INTO customers (chat_id) VALUES ('dup')`); err != nil {
		t.Fatalf("dup customer: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (customer_id) VALUES ('dup')`); err != nil {
		t.Fatalf("first open cart: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders (customer_id) VALUES ('dup')`); err == nil {
		t.Fatal("a second open cart for one customer must be rejected")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM order_items; DELETE FROM orders; DELETE FROM customers`); err != nil {
		t.Fatalf("clear migration fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO menu_items (name, price_cents) VALUES ('Nasi Goreng', 25000), ('Es Teh', 5000)`); err != nil {
		t.Fatalf("seed menu: %v", err)
	}

	// Wire the real stack, with only Telegram faked.
	const (
		botToken = "TEST-BOT-TOKEN"
		secret   = "test-secret"
		kdsToken = "test-kds-token-0123456789abcdef0123"
		chatID   = int64(424242)
	)
	tg, sent := newFakeTelegram(t, botToken)

	hub := NewHub(kdsToken, []string{"https://kds.example.com"})
	t.Cleanup(hub.Close)
	notifier := NewTelegramNotifier(botToken)
	notifier.baseURL = tg.URL
	store := NewPostgresStorage(pool)
	engine := NewEngine(store, hub, notifier, Currency{Code: "IDR", Decimals: 0})
	hub.SetKitchenUpdater(engine)
	app := &App{engine: engine, notifier: notifier, ledger: store}

	srv := httptest.NewServer(newRouter(app, hub, secret))
	t.Cleanup(srv.Close)

	// Router allowlisting and webhook authentication.
	if resp, err := http.Get(srv.URL + "/admin"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unregistered route should return 404, got %v (err %v)", resp, err)
	}
	if resp, err := http.Post(srv.URL+"/webhook", "application/json", strings.NewReader(`{}`)); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("webhook without the secret should return 401, got %v (err %v)", resp, err)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	// The kitchen socket refuses anyone without the token, and any browser origin that is not listed.
	expectHandshakeStatus := func(label string, dialer *websocket.Dialer, header http.Header, want int) {
		t.Helper()
		conn, resp, err := dialer.Dial(wsURL, header)
		if err == nil {
			conn.Close()
			t.Fatalf("%s: handshake unexpectedly succeeded", label)
		}
		if resp == nil || resp.StatusCode != want {
			t.Fatalf("%s: handshake response = %v, want status %d (err %v)", label, resp, want, err)
		}
	}
	expectHandshakeStatus("no token", websocket.DefaultDialer, nil, http.StatusUnauthorized)
	expectHandshakeStatus("wrong token", websocket.DefaultDialer,
		http.Header{"Authorization": {"Bearer wrong-token-wrong-token-wrong-token"}}, http.StatusUnauthorized)
	expectHandshakeStatus("foreign origin", websocket.DefaultDialer,
		http.Header{"Authorization": {"Bearer " + kdsToken}, "Origin": {"https://evil.example.com"}}, http.StatusForbidden)

	// A browser style client authenticates through the subprotocol and gets kds.v1 back.
	browser := websocket.Dialer{Subprotocols: []string{"kds.v1", "kds-token." + kdsToken}}
	bconn, _, err := browser.Dial(wsURL, http.Header{"Origin": {"https://kds.example.com"}})
	if err != nil {
		t.Fatalf("browser style dial: %v", err)
	}
	if got := bconn.Subprotocol(); got != "kds.v1" {
		t.Fatalf("negotiated subprotocol = %q, want kds.v1 (the token must not be echoed back)", got)
	}
	bconn.Close()

	waitForClients := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for hub.clientCount() != want {
			if time.Now().After(deadline) {
				t.Fatalf("hub has %d clients, want %d", hub.clientCount(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForClients(0) // the closed browser style connection is removed

	// The kitchen display (a non-browser client with a bearer header) connects before any order exists.
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + kdsToken}})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	waitForClients(1)

	post := func(updateID int, text string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]interface{}{
			"update_id": updateID,
			"message": map[string]interface{}{
				"chat": map[string]interface{}{"id": chatID},
				"text": text,
			},
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhook", bytes.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("webhook %q: %v", text, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	lastUpdateID := 0
	say := func(text string) string {
		t.Helper()
		lastUpdateID++
		if code := post(lastUpdateID, text); code != http.StatusOK {
			t.Fatalf("webhook %q returned %d", text, code)
		}
		m := waitForMessage(t, sent)
		if m.ChatID != chatID {
			t.Fatalf("reply to %q went to chat %d, want %d", text, m.ChatID, chatID)
		}
		return m.Text
	}

	steps := []struct{ send, wantReply string }{
		{"/start", "- Nasi Goreng: IDR 25,000"},
		{"2x Nasi Goreng", "Added: 2x Nasi Goreng (IDR 50,000)"},
		{"es teh", "Added: 1x Es Teh (IDR 5,000)"},
		{"pizza", "'pizza' is not on the menu"},
		{"done", "Total: IDR 55,000"},
		{"yes", "delivery address"},
		{"Jl. Melati 5", "Address confirmed: Jl. Melati 5"},
	}
	for _, s := range steps {
		if reply := say(s.send); !strings.Contains(reply, s.wantReply) {
			t.Fatalf("after %q got reply %q, want it to contain %q", s.send, reply, s.wantReply)
		}
	}

	// The confirmed order reaches the kitchen display.
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("kitchen never received NEW_ORDER: %v", err)
	}
	newOrder := string(raw)
	for _, want := range []string{"NEW_ORDER|ID:", "Customer:424242", "Address:Jl. Melati 5", "Items:2x Nasi Goreng, 1x Es Teh"} {
		if !strings.Contains(newOrder, want) {
			t.Fatalf("NEW_ORDER message %q is missing %q", newOrder, want)
		}
	}
	orderID, err := strconv.Atoi(strings.TrimPrefix(strings.Split(newOrder, "|")[1], "ID:"))
	if err != nil {
		t.Fatalf("cannot parse order id from %q: %v", newOrder, err)
	}

	statusOf := func() string {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status); err != nil {
			t.Fatalf("read order status: %v", err)
		}
		return status
	}
	if got := statusOf(); got != "confirmed" {
		t.Fatalf("order status after checkout = %q, want confirmed", got)
	}

	// The total and every line price were recorded when the items were added, so a later menu change
	// does not touch the order.
	totalOf := func() int64 {
		t.Helper()
		var total int64
		if err := pool.QueryRow(ctx, `SELECT total_cents FROM orders WHERE id = $1`, orderID).Scan(&total); err != nil {
			t.Fatalf("read order total: %v", err)
		}
		return total
	}
	if _, err := pool.Exec(ctx, `UPDATE menu_items SET price_cents = 99999 WHERE name = 'Nasi Goreng'`); err != nil {
		t.Fatalf("change menu price: %v", err)
	}
	if got := totalOf(); got != 55000 {
		t.Fatalf("stored total = %d after a menu price change, want 55000", got)
	}
	rows, err := pool.Query(ctx, `SELECT item_name, quantity, unit_price_cents FROM order_items WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		t.Fatalf("read order lines: %v", err)
	}
	var lines []OrderLine
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.Name, &l.Quantity, &l.UnitPriceCents); err != nil {
			t.Fatalf("scan order line: %v", err)
		}
		lines = append(lines, l)
	}
	rows.Close()
	wantLines := []OrderLine{{"Nasi Goreng", 2, 25000}, {"Es Teh", 1, 5000}}
	if len(lines) != len(wantLines) || lines[0] != wantLines[0] || lines[1] != wantLines[1] {
		t.Fatalf("order lines = %+v, want %+v", lines, wantLines)
	}

	// Telegram delivers the last update again, as it does after a timeout. Nothing happens twice.
	if code := post(lastUpdateID, "Jl. Melati 5"); code != http.StatusOK {
		t.Fatalf("redelivered update returned %d, want 200", code)
	}
	select {
	case m := <-sent:
		t.Fatalf("a redelivered update produced another reply: %+v", m)
	case <-time.After(400 * time.Millisecond):
	}
	if got := count(`SELECT count(*) FROM orders WHERE customer_id = $1`, fmt.Sprint(chatID)); got != 1 {
		t.Fatalf("customer has %d orders after a redelivery, want 1", got)
	}
	if got := count(`SELECT count(*) FROM processed_updates WHERE update_id = $1`, lastUpdateID); got != 1 {
		t.Fatalf("ledger has %d rows for the update, want 1", got)
	}

	// The kitchen moves the order along; each step updates PostgreSQL and
	// sends a Telegram message to the original chat.
	kitchen := []struct{ status, wantText string }{
		{"preparing", "being prepared"},
		{"ready", "ready for delivery"},
		{"completed", "is complete"},
	}
	for _, k := range kitchen {
		payload, _ := json.Marshal(KDSAction{OrderID: orderID, Status: k.status})
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatalf("send %s: %v", k.status, err)
		}
		m := waitForMessage(t, sent)
		if m.ChatID != chatID || !strings.Contains(m.Text, k.wantText) {
			t.Fatalf("status %q produced message %+v, want chat %d containing %q", k.status, m, chatID, k.wantText)
		}
		if got := statusOf(); got != k.status {
			t.Fatalf("database status = %q, want %q", got, k.status)
		}
	}

	// Unknown statuses, statuses the kitchen may not set, and replays of a finished step all change
	// nothing and notify nobody.
	for _, status := range []string{"bogus", "cancelled", "completed", "preparing"} {
		payload, _ := json.Marshal(KDSAction{OrderID: orderID, Status: status})
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatalf("send %q: %v", status, err)
		}
	}
	select {
	case m := <-sent:
		t.Fatalf("a rejected update triggered a notification: %+v", m)
	case <-time.After(500 * time.Millisecond):
	}
	if got := statusOf(); got != "completed" {
		t.Fatalf("status after invalid updates = %q, want completed", got)
	}
}
