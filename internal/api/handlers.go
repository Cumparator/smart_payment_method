package api

import (
	"encoding/json"
	"net/http"

	"github.com/Cumparator/smart_payment_method/internal/payment"
	"github.com/Cumparator/smart_payment_method/internal/store"
)

type Handler struct {
	Store *store.DB
}

func New(s *store.DB) *Handler { return &Handler{Store: s} }

type createReq struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (h *Handler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key required", http.StatusBadRequest)
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	p := payment.Payment{
		ID:             newID(),
		IdempotencyKey: key,
		Amount:         req.Amount,
		Currency:       req.Currency,
		State:          payment.Received,
		NextRunAt:      timeNow(),
	}
	created, exists, err := h.Store.CreatePayment(r.Context(), p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if exists && created.Amount != req.Amount {
		http.Error(w, "idempotency key reused with different amount", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(created)
}

func (h *Handler) GetPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.Store.GetPayment(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// helpers
func newID() string { return generateUUID() }
