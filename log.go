package cowallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/fox-one/mixin-sdk-go/v2"
)

const (
	spendOffsetProperty = "spend_offset"
)

func (s *Server) ListPendingLogs(ctx context.Context) error {
	for {
		_ = s.listPendingLogs(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) listPendingLogs(ctx context.Context) error {
	const limit = 100
	logs, err := ListLogs(s.db, limit)
	if err != nil {
		slog.Error("ListLogs", "error", err)
		return err
	}

	for _, log := range logs {
		if err := s.handleLog(ctx, log); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) handleLog(ctx context.Context, log *Log) error {
	opt := mixin.SafeListUtxoOption{
		Members:   []string{s.client.ClientID},
		Threshold: 1,
		Limit:     1,
		State:     mixin.SafeUtxoStateUnspent,
	}

	if err := ReadProperty(s.db, spendOffsetProperty, &opt.Offset); err != nil {
		slog.Error("ReadProperty", "error", err)
		return err
	}

	outputs, err := s.client.SafeListUtxos(ctx, opt)
	if err != nil {
		slog.Error("SafeListUtxos", "err", err)
		return err
	}

	if len(outputs) == 0 {
		slog.Error("unspent utxo dry")
		return fmt.Errorf("unspent utxo dry with offset %d", opt.Offset)
	}

	utxo := outputs[0]
	b := mixin.NewSafeTransactionBuilder([]*mixin.SafeUtxo{utxo})
	b.Hint = log.ID.String()
	b.Memo = string(log.Raw)
	receiver := &mixin.TransactionOutput{
		Address: mixin.RequireNewMixAddress(opt.Members, opt.Threshold),
		Amount:  utxo.Amount,
	}

	tx, err := s.client.MakeTransaction(ctx, b, []*mixin.TransactionOutput{receiver})
	if err != nil {
		return fmt.Errorf("make transaction failed: %w", err)
	}

	raw, err := tx.Dump()
	if err != nil {
		return fmt.Errorf("tx dump failed: %w", err)
	}

	// prepare transaction
	req, err := s.client.SafeCreateTransactionRequest(ctx, &mixin.SafeTransactionRequestInput{
		RequestID:      log.ID.String(),
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
		RequestID:      log.ID.String(),
		RawTransaction: hex.EncodeToString(data),
	}); err != nil {
		return fmt.Errorf("submit transaction failed: %w", err)
	}

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	if err := deleteLog(txn, log.ID); err != nil {
		return err
	}

	if err := saveProperty(txn, spendOffsetProperty, utxo.Sequence+1); err != nil {
		return err
	}

	return txn.Commit()
}
