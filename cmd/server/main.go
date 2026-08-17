package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"wallet-transfer/internal/db"
	"wallet-transfer/internal/handler"
	"wallet-transfer/internal/repository"
	"wallet-transfer/internal/service"
)

func main() {
	// Load .env if present; ignore the error — env vars may be set externally.
	_ = godotenv.Load()

	ctx := context.Background()

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	// Wire dependencies.
	walletRepo := repository.NewWalletRepository()
	transferRepo := repository.NewTransferRepository()
	ledgerRepo := repository.NewLedgerRepository()
	idempotencyRepo := repository.NewIdempotencyRepository()

	transferSvc := service.NewTransferService(pool, walletRepo, transferRepo, ledgerRepo, idempotencyRepo)

	transferHandler := handler.NewTransferHandler(transferSvc)
	walletHandler := handler.NewWalletHandler(pool, walletRepo)

	// Routes.
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Post("/transfers", transferHandler.CreateTransfer)
	r.Get("/wallets/{id}", walletHandler.GetWallet)

	port := getEnv("PORT", "8080")
	log.Printf("server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
