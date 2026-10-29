// Command server — HTTP API, воркеры и сверщик в одном процессе.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/Cumparator/smart_payment_method/internal/api"
	"github.com/Cumparator/smart_payment_method/internal/config"
	"github.com/Cumparator/smart_payment_method/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	db, err := store.New(context.Background(), cfg.Database)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	h := api.New(db)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /payments", h.CreatePayment)
	mux.HandleFunc("GET /payments/{id}", h.GetPayment)

	slog.Info("listening", "addr", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
