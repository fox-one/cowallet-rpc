package cowallet

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	outputOffsetProperty = "spend_offset"
)

func (s *Server) LoopOutputs(ctx context.Context) error {
	for {
		_ = s.loopOutputs(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) loopOutputs(ctx context.Context) error {
	opt := mixin.SafeListUtxoOption{
		Members:   []string{s.client.ClientID},
		Threshold: 1,
		Limit:     500,
	}

	if err := ReadProperty(s.db, outputOffsetProperty, &opt.Offset); err != nil {
		return err
	}

	outputs, err := s.client.SafeListUtxos(ctx, opt)
	if err != nil {
		slog.Error("SafeListUtxos", "err", err)
		return err
	}

	if len(outputs) == 0 {
		return nil
	}

	slog.Info("SafeListUtxos", "count", len(outputs), "offset", opt.Offset)

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	for _, output := range outputs {
		if err := s.handleOutput(ctx, txn, output); err != nil {
			slog.Error("handleOutput", "err", err)
			return err
		}

		if err := saveProperty(txn, outputOffsetProperty, output.Sequence+1); err != nil {
			return err
		}
	}

	return txn.Commit()
}

func (s *Server) handleOutput(ctx context.Context, txn *badger.Txn, output *mixin.SafeUtxo) error {
	if output.OutputIndex > 0 {
		return nil
	}

	slog.Info(
		"handle output",
		"seq", output.Sequence,
		"extra", output.Extra,
		"asset", output.AssetID,
		"amount", output.Amount,
	)

	b, err := hex.DecodeString(output.Extra)
	if err != nil {
		return nil
	}

	addr, err := mixin.MixAddressFromString(string(b))
	if err != nil {
		return nil
	}

	slog.Info("renew vault", "addr", addr.String())

	if output.State != mixin.SafeUtxoStateUnspent {
		req, err := s.client.SafeReadTransactionRequest(ctx, output.SignedBy)
		if err != nil {
			slog.Error("SafeReadTransactionRequest", "err", err)
			return err
		}

		extra, err := hex.DecodeString(req.Extra)
		if err != nil {
			return nil
		}

		var (
			id     uuid.UUID
			period int64
		)

		if err := decodeIndexKey(extra, renewPrefix, &id, &period); err != nil {
			return nil
		}

		if id.String() != output.OutputID {
			return nil
		}

		return s.renewVault(txn, output, addr, period)
	}

	period := s.getRenewPeriod(output)
	if period <= 0 {
		return nil
	}

	extra := buildIndexKey(renewPrefix, uuid.MustParse(output.OutputID), period)
	if err := s.submit(ctx, output, output.OutputID, string(extra)); err != nil {
		return err
	}

	return s.renewVault(txn, output, addr, period)
}

func (s *Server) getRenewPeriod(utxo *mixin.SafeUtxo) int64 {
	if utxo.AssetID != s.cfg.PayAssetID {
		return 0
	}

	const month = 30 * 24 * time.Hour
	base := decimal.NewFromFloat(month.Seconds())
	return utxo.Amount.Div(s.cfg.PayAmount).Mul(base).IntPart()
}

func (s *Server) renewVault(txn *badger.Txn, output *mixin.SafeUtxo, addr *mixin.MixAddress, period int64) error {
	from, seq, err := getVaultExpiredAt(txn, addr.Members(), addr.Threshold)
	if err != nil {
		slog.Error("getVaultExpiredAt", "err", err)
		return err
	}

	if seq >= output.Sequence {
		return nil
	}

	from = maxDate(from, output.CreatedAt)

	r := &Renew{
		ID:        uuid.MustParse(output.RequestID),
		CreatedAt: output.CreatedAt,
		Sequence:  output.Sequence,
		Members:   addr.Members(),
		Threshold: addr.Threshold,
		Asset:     output.AssetID,
		Amount:    output.Amount,
		Period:    period,
		From:      from,
		To:        from.Add((time.Duration(period) * time.Second)),
	}

	if len(output.Senders) > 0 {
		sender := mixin.RequireNewMixAddress(output.Senders, output.SendersThreshold)
		r.Sender = sender.String()
	}

	if err := saveRenew(txn, r); err != nil {
		return err
	}

	return nil
}
