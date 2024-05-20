package cowallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/pandodao/mtg/mtgpack"
)

const (
	spendOffsetProperty = "spend_offset"
)

func (s *Server) NextLog(action uint8, trace string, args ...any) (*Log, error) {
	seq, err := s.seq.Next()
	if err != nil {
		return nil, err
	}

	enc := mtgpack.NewEncoder()
	if err := enc.EncodeUint8(action); err != nil {
		return nil, err
	}

	if err := enc.EncodeValues(args...); err != nil {
		return nil, err
	}

	v := &Log{
		Seq:       seq,
		CreatedAt: time.Now(),
		TraceID:   trace,
		Memo:      string(enc.Bytes()),
	}

	return v, nil
}

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
	if _, err := s.client.SafeReadTransactionRequest(ctx, log.TraceID); err == nil {
		return nil
	} else if !mixin.IsErrorCodes(err, mixin.EndpointNotFound) {
		return err
	}

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
	b.Hint = log.TraceID
	b.Memo = log.Memo
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
		RequestID:      log.TraceID,
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
		RequestID:      log.TraceID,
		RawTransaction: hex.EncodeToString(data),
	}); err != nil {
		return fmt.Errorf("submit transaction failed: %w", err)
	}

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	if err := deleteLog(txn, log.Seq); err != nil {
		return err
	}

	if err := saveProperty(txn, spendOffsetProperty, utxo.Sequence+1); err != nil {
		return err
	}

	return txn.Commit()
}
