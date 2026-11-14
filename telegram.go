package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	telegramAPIBase = "https://api.telegram.org"
	maxWebhookBody  = 1 << 20 // Telegram updates are a few KB; 1 MiB leaves ample headroom
)

// TelegramNotifier implements the Notifier port with outbound calls to the Telegram Bot API.
// It keeps all Telegram specifics out of domain.go.
type TelegramNotifier struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewTelegramNotifier(token string) *TelegramNotifier {
	return &TelegramNotifier{
		token:   token,
		baseURL: telegramAPIBase,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// SendNotification posts message to the chat identified by customerID, which is the Telegram
// chat_id stored as a string.
func (t *TelegramNotifier) SendNotification(customerID string, message string) error {
	var chatID interface{} = customerID
	if id, err := strconv.ParseInt(customerID, 10, 64); err == nil {
		chatID = id
	}

	body, err := json.Marshal(map[string]interface{}{
		"chat_id": chatID,
		"text":    message,
	})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	resp, err := t.client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		// net/http wraps failures in a *url.Error that carries the full URL, which contains the
		// bot token. Drop the URL before the error reaches a log line.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("telegram sendMessage request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram sendMessage returned status %d: %s", resp.StatusCode, detail)
	}
	return nil
}

// UpdateLedger remembers which Telegram updates were handled. Telegram redelivers an update when
// the webhook answers with an error or times out, and adding an item is not safe to repeat.
type UpdateLedger interface {
	UpdateSeen(ctx context.Context, updateID int64) (bool, error)
	RecordUpdate(ctx context.Context, updateID int64) error
}

type TelegramWebhook struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type App struct {
	engine   *Engine
	notifier Notifier
	ledger   UpdateLedger // optional; without it, redelivered updates are processed again
}

func (app *App) telegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)

	var payload TelegramWebhook
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Payload Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	chatID := payload.Message.Chat.ID
	if chatID == 0 {
		// Updates without a message (edited messages, membership changes) decode to zero values.
		// Acknowledge them so Telegram does not retry, and do nothing else. Without this guard
		// they would create a customer row with chat_id 0.
		w.WriteHeader(http.StatusOK)
		return
	}

	track := app.ledger != nil && payload.UpdateID != 0
	if track {
		seen, err := app.ledger.UpdateSeen(r.Context(), payload.UpdateID)
		if err != nil {
			log.Printf("Update ledger lookup failed: %v\n", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if seen {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	cmd := Command{
		CustomerID: fmt.Sprintf("%d", chatID),
		Text:       payload.Message.Text,
	}

	replyText, err := app.engine.Process(r.Context(), cmd)
	if err != nil {
		// Not recorded, so Telegram's retry gets another chance to succeed.
		log.Printf("Engine Processing Error: %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if track {
		if err := app.ledger.RecordUpdate(r.Context(), payload.UpdateID); err != nil {
			log.Printf("Failed to record update %d: %v\n", payload.UpdateID, err)
		}
	}

	if err := app.notifier.SendNotification(cmd.CustomerID, replyText); err != nil {
		log.Printf("Failed to send reply: %v\n", err)
	}
	w.WriteHeader(http.StatusOK)
}

func telegramAuthMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !secureEqual(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"), secret) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
