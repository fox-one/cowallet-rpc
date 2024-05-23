package cowallet

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/fox-one/mixin-sdk-go/v2"
)

func (s *Server) submit(ctx context.Context, utxo *mixin.SafeUtxo, id, msg string) error {
	b := mixin.NewSafeTransactionBuilder([]*mixin.SafeUtxo{utxo})
	b.Hint = id
	b.Memo = msg

	tx, err := s.client.MakeTransaction(ctx, b, nil)
	if err != nil {
		return fmt.Errorf("make transaction failed: %w", err)
	}

	raw, err := tx.Dump()
	if err != nil {
		return fmt.Errorf("tx dump failed: %w", err)
	}

	// prepare transaction
	req, err := s.client.SafeCreateTransactionRequest(ctx, &mixin.SafeTransactionRequestInput{
		RequestID:      id,
		RawTransaction: raw,
	})

	if err != nil {
		return fmt.Errorf("create transaction request failed: %w", err)
	}

	// sign transaction
	if err := mixin.SafeSignTransaction(tx, s.cfg.SpendKey, req.Views, 0); err != nil {
		return fmt.Errorf("sign transaction failed: %w", err)
	}

	data, err := tx.DumpData()
	if err != nil {
		return fmt.Errorf("tx dump data failed: %w", err)
	}

	// submit transaction
	if _, err := s.client.SafeSubmitTransactionRequest(ctx, &mixin.SafeTransactionRequestInput{
		RequestID:      id,
		RawTransaction: hex.EncodeToString(data),
	}); err != nil {
		return fmt.Errorf("submit transaction failed: %w", err)
	}

	return nil
}
