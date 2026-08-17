package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"wallet-transfer/internal/domain"
	"wallet-transfer/internal/service"
)

// TransferHandler handles HTTP requests for the transfer resource.
// It is responsible for request validation and transport mapping only —
// no business logic lives here.
type TransferHandler struct {
	svc *service.TransferService
}

func NewTransferHandler(svc *service.TransferService) *TransferHandler {
	return &TransferHandler{svc: svc}
}

type createTransferRequest struct {
	IdempotencyKey string  `json:"idempotencyKey"`
	FromWalletID   string  `json:"fromWalletId"`
	ToWalletID     string  `json:"toWalletId"`
	Amount         float64 `json:"amount"`
}

// CreateTransfer handles POST /transfers.
func (h *TransferHandler) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	var body createTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	defer r.Body.Close()

	if body.IdempotencyKey == "" || body.FromWalletID == "" || body.ToWalletID == "" {
		writeError(w, http.StatusUnprocessableEntity, "idempotencyKey, fromWalletId, and toWalletId are required")
		return
	}
	if body.Amount <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "amount must be greater than zero")
		return
	}

	resp, err := h.svc.CreateTransfer(r.Context(), service.CreateTransferRequest{
		IdempotencyKey: body.IdempotencyKey,
		FromWalletID:   body.FromWalletID,
		ToWalletID:     body.ToWalletID,
		Amount:         body.Amount,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInsufficientFunds):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, domain.ErrWalletNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, domain.ErrSameWallet), errors.Is(err, domain.ErrInvalidAmount):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			log.Printf("CreateTransfer internal error: %v", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
