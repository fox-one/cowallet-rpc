package cowallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/google/uuid"
	"github.com/pandodao/mtg/mtgpack"
	"github.com/pandodao/mtg/protocol"
	"github.com/shopspring/decimal"
)

func (s *Server) handleOutput(_ context.Context, txn *badger.Txn, output *mixin.SafeUtxo) error {
	if output.OutputIndex > 0 {
		return nil
	}

	b, _ := hex.DecodeString(output.Extra)

	if addr, err := mixin.MixAddressFromString(string(b)); err == nil {
		return s.renewVault(txn, output, addr)
	}

	// system log
	if len(output.Senders) == 1 && output.Senders[0] == s.client.ClientID {

	}

	return nil
}

func (s *Server) renewVault(txn *badger.Txn, output *mixin.SafeUtxo, addr *mixin.MixAddress) error {
	id := uuid.NewSHA1(
		uuid.MustParse(output.RequestID),
		[]byte("renew"),
	)

	r := &Renew{
		ID:        id,
		CreatedAt: output.CreatedAt,
		Members:   addr.Members(),
		Threshold: addr.Threshold,
		Asset:     output.AssetID,
		Amount:    output.Amount,
	}

	if len(output.Senders) > 0 {
		sender := mixin.RequireNewMainnetMixAddress(output.Senders, output.SendersThreshold)
		r.Sender = sender.String()
	}

	if err := saveRenew(txn, r); err != nil {
		return err
	}

	if s.cfg.PayAssetID != output.AssetID {
		return nil
	}

	const month = time.Hour * 24 * 30
	base := decimal.NewFromFloat(month.Seconds())
	dur := base.Mul(output.Amount).Div(s.cfg.PayAmount).IntPart()
	if dur <= 0 {
		return nil
	}

	return createLog(
		txn,
		id,
		CommandRenewVault,
		newMultisigReceiver(addr.Members(), addr.Threshold),
		dur,
	)
}

func (s *Server) handleSystemCommand(txn *badger.Txn, output *mixin.SafeUtxo, b []byte) error {
	dec := mtgpack.NewDecoder(b)

	action, err := dec.DecodeUint8()
	if err != nil {
		return fmt.Errorf("decode action failed: %w", err)
	}

	switch action {
	case CommandRenewVault:
		var (
			r   protocol.MultisigReceiver
			dur int64
		)

		if err := dec.DecodeValues(&r, &dur); err != nil {
			return err
		}

		v, err := findVault(txn, multisigMembers(r), r.Threshold)
		if err != nil {
			return err
		}

		if output.CreatedAt.After(v.ExpiredAt) {
			v.ExpiredAt = output.CreatedAt
		}

		v.ExpiredAt = v.ExpiredAt.Add(time.Duration(dur) * time.Second)
		return saveVault(txn, v)
	case CommandAddAddress, CommandDelAddress:
		var (
			user  uuid.UUID
			r     protocol.MultisigReceiver
			label string
		)

		if err := dec.DecodeValues(&user, &r, &label); err != nil {
			return err
		}

		
	}
}

func newMultisigReceiver(members []string, threshold uint8) protocol.MultisigReceiver {
	var r protocol.MultisigReceiver
	r.Threshold = threshold
	for _, id := range members {
		r.Members = append(r.Members, uuid.MustParse(id))
	}

	return r
}

func multisigMembers(r protocol.MultisigReceiver) []string {
	var members []string
	for _, id := range r.Members {
		members = append(members, id.String())
	}

	return members
}
