package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type TelegramWebhook struct {
	Message struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type App struct {
	engine   *Engine
	BotToken string
}

func (app *App) sendMessage(chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", app.BotToken)

	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	jsonData, _ := json.Marshal(payload)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Failed to send message: %v\n", err)
		return
	}
	defer resp.Body.Close()
}

func (app *App) telegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var payload TelegramWebhook

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	chatID := payload.Message.Chat.ID

	cmd := Command{
		CustomerID: fmt.Sprintf("%d", chatID),
		Text:       payload.Message.Text,
	}

	replyText, err := app.engine.Process(r.Context(), cmd)
	if err != nil {
		log.Printf("Engine Processing Error: %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	app.sendMessage(chatID, replyText)
	w.WriteHeader(http.StatusOK)
}

func telegramAuthMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secret {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file")
	}

	dbURL := os.Getenv("DATABASE_URL")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	hub := NewHub()
	go hub.Run()

	store := &PostgresStorage{db: pool}
	engine := &Engine{
		store: store,
		bcast: hub,
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	app := &App{
		engine:   engine,
		BotToken: botToken,
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	telegramSecret := os.Getenv("TELEGRAM_SECRET_TOKEN")
	r.Group(func(r chi.Router) {
		r.Use(telegramAuthMiddleware(telegramSecret))
		r.Post("/webhook", app.telegramWebhookHandler)
	})
	r.Get("/ws", hub.HandleWebSocket)

	fmt.Println("omnipos server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
