package domain

import "errors"

var (
	ErrWalletNotFound    = errors.New("wallet not found")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrSameWallet        = errors.New("source and destination wallet must be different")
	ErrInvalidAmount     = errors.New("amount must be greater than zero")
	ErrTransferNotFound  = errors.New("transfer not found")
)
