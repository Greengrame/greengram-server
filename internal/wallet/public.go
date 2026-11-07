package wallet

import (
	"context"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"
)

// TONBalance is the user's balance in the wallet's main coin (GRAM on screen).
func (s *Service) TONBalance(ctx context.Context, user int64) (*big.Int, error) {
	var units string
	err := s.db.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", user).Scan(&units)
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil
	}
	if err != nil {
		return nil, err
	}
	n, _ := new(big.Int).SetString(units, 10)
	return n, nil
}
