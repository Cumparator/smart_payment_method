// Command server — HTTP API, воркеры и сверщик в одном процессе.
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("SPM_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// TODO: POST /payments, GET /payments/{id}, GET /payments/{id}/timeline,
	// GET /channels, POST /channels/{id}/outage, POST /experiments.
	// TODO: запуск воркеров, сверщика и уборщика зависших STARTED.

	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
