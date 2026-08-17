package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-transfer/internal/domain"
	"wallet-transfer/internal/repository"
)

// WalletHandler handles HTTP requests for the wallet resource.
type WalletHandler struct {
	pool       *pgxpool.Pool
	walletRepo *repository.WalletRepository
}

func NewWalletHandler(pool *pgxpool.Pool, walletRepo *repository.WalletRepository) *WalletHandler {
	return &WalletHandler{pool: pool, walletRepo: walletRepo}
}

// GetWallet handles GET /wallets/{id}.
func (h *WalletHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "wallet id is required")
		return
	}

	wallet, err := h.walletRepo.FindByID(r.Context(), h.pool, id)
	if err != nil {
		if errors.Is(err, domain.ErrWalletNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("GetWallet internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, wallet)
}
