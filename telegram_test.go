package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookIgnoresUpdatesWithoutAMessage(t *testing.T) {
	// A nil engine proves the handler returns before it touches the domain.
	app := &App{}
	body := `{"update_id":1,"edited_message":{"chat":{"id":555},"text":"hi"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	app.telegramWebhookHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 so Telegram does not retry", rec.Code)
	}
}

func TestWebhookRejectsOversizedBodies(t *testing.T) {
	app := &App{}
	body := `{"message":{"chat":{"id":1},"text":"` + strings.Repeat("a", 2*maxWebhookBody) + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	app.telegramWebhookHandler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

type fakeLedger struct {
	recorded    map[int64]bool
	lookups     int
	recordCalls int
}

func newFakeLedger() *fakeLedger { return &fakeLedger{recorded: map[int64]bool{}} }

func (l *fakeLedger) UpdateSeen(_ context.Context, id int64) (bool, error) {
	l.lookups++
	return l.recorded[id], nil
}

func (l *fakeLedger) RecordUpdate(_ context.Context, id int64) error {
	l.recordCalls++
	l.recorded[id] = true
	return nil
}

func postUpdate(app *App, updateID int64, chatID int64, text string) int {
	body := fmt.Sprintf(`{"update_id":%d,"message":{"chat":{"id":%d},"text":%q}}`, updateID, chatID, text)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.telegramWebhookHandler(rec, req)
	return rec.Code
}

func TestWebhookIgnoresRedeliveredUpdates(t *testing.T) {
	h := newHarness(t)
	ledger := newFakeLedger()
	app := &App{engine: h.eng, notifier: h.nt, ledger: ledger}

	if code := postUpdate(app, 10, 555, "/start"); code != http.StatusOK {
		t.Fatalf("first delivery status = %d", code)
	}
	if code := postUpdate(app, 10, 555, "/start"); code != http.StatusOK {
		t.Fatalf("redelivery must still answer 200 so Telegram stops retrying, got %d", code)
	}
	if len(h.nt.sent) != 1 {
		t.Fatalf("customer received %d replies, want 1", len(h.nt.sent))
	}

	// An item added twice would double the order, which is exactly what deduplication prevents.
	postUpdate(app, 11, 555, "2x Es Teh")
	postUpdate(app, 11, 555, "2x Es Teh")
	if got := len(h.store.orders[0].lines); got != 1 {
		t.Fatalf("order has %d lines after a redelivered item, want 1", got)
	}
}

func TestWebhookRetriesAfterAFailure(t *testing.T) {
	h := newHarness(t)
	ledger := newFakeLedger()
	app := &App{engine: h.eng, notifier: h.nt, ledger: ledger}

	h.store.failGetState = true
	if code := postUpdate(app, 20, 555, "/start"); code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so Telegram retries", code)
	}
	if ledger.recorded[20] {
		t.Fatal("a failed update must not be recorded, or the retry would be skipped")
	}

	h.store.failGetState = false
	if code := postUpdate(app, 20, 555, "/start"); code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", code)
	}
	if !ledger.recorded[20] || len(h.nt.sent) != 1 {
		t.Fatalf("retry should be processed once: recorded=%v replies=%d", ledger.recorded[20], len(h.nt.sent))
	}
}

func TestWebhookWithoutUpdateIDSkipsTheLedger(t *testing.T) {
	h := newHarness(t)
	ledger := newFakeLedger()
	app := &App{engine: h.eng, notifier: h.nt, ledger: ledger}

	postUpdate(app, 0, 555, "/start")

	if ledger.lookups != 0 || ledger.recordCalls != 0 {
		t.Fatalf("ledger used for an update without an id: lookups=%d records=%d", ledger.lookups, ledger.recordCalls)
	}
	if len(h.nt.sent) != 1 {
		t.Fatalf("the update should still be processed, replies=%d", len(h.nt.sent))
	}
}

func TestWebhookRejectsMalformedJSON(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()

	app.telegramWebhookHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
