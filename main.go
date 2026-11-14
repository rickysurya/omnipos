package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// newRouter registers the only routes the service answers. Anything else falls through to chi's
// default 404. middleware.RealIP is deliberately not used: it trusts client supplied headers.
func newRouter(app *App, hub *Hub, telegramSecret string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Group(func(r chi.Router) {
		r.Use(telegramAuthMiddleware(telegramSecret))
		r.Post("/webhook", app.telegramWebhookHandler)
	})
	r.Get("/ws", hub.HandleWebSocket)

	return r
}

func run() error {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file loaded, using the process environment")
	}

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPing()
	if err := pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("reach database: %w", err)
	}

	hub := NewHub(cfg.KDSToken, cfg.KDSAllowedOrigins)
	store := NewPostgresStorage(pool)
	notifier := NewTelegramNotifier(cfg.BotToken)
	engine := NewEngine(store, hub, notifier, Currency{Code: cfg.Currency, Decimals: cfg.CurrencyDecimals})
	hub.SetKitchenUpdater(engine)

	app := &App{engine: engine, notifier: notifier, ledger: store}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           newRouter(app, hub, cfg.TelegramSecret),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	// Shutdown does not touch hijacked connections, so close the kitchen displays explicitly.
	srv.RegisterOnShutdown(hub.Close)

	serveErr := make(chan error, 1)
	go func() {
		fmt.Printf("omnipos server listening on :%s\n", cfg.Port)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
