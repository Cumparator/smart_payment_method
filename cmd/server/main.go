// Command server — HTTP API, воркеры и сверщик в одном процессе.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/Cumparator/smart_payment_method/internal/config"
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// TODO: POST /payments, GET /payments/{id}, GET /payments/{id}/timeline,
	// GET /channels, POST /channels/{id}/outage, POST /experiments.
	// TODO: запуск воркеров, сверщика и уборщика зависших STARTED.

	slog.Info("listening", "addr", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
