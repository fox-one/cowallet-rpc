package cowallet

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go"
	"github.com/fox-one/pando-protos/cowallet/v1"
	g "github.com/pandodao/generic"
	"github.com/shopspring/decimal"
	"github.com/spf13/cast"
	"github.com/twitchtv/twirp"
	"github.com/zyedidia/generic/mapset"
)

func (s *Server) HandleRPC() http.Handler {
	h := cowallet.NewCoWalletServiceServer(s)
	return handleAuth(s.cfg.Issuer)(h)
}

func (s *Server) GetMultisigGroup(ctx context.Context, req *cowallet.GetMultisigGroupRequest) (*cowallet.GetMultisigGroupResponse, error) {
	user, ok := UserFrom(ctx)
	if !ok {
		return nil, twirp.Unauthenticated.Error("auth required")
	}

	if !govalidator.IsIn(user.MixinID, req.Members...) {
		return nil, twirp.PermissionDenied.Error("permission denied")
	}

	if err := s.db.Update(func(txn *badger.Txn) error {
		return saveJob(txn, &Job{
			CreatedAt: time.Now(),
			Token:     user.Token,
			Members:   req.Members,
			Threshold: uint8(req.Threshold),
		})
	}); err != nil {
		slog.Error("rpc: save job failed", slog.Any("err", err))
		return nil, err
	}

	resp := &cowallet.GetMultisigGroupResponse{}
	if err := s.db.View(func(txn *badger.Txn) error {
		balances, transactions, err := groupUTXO(txn, req.Members, uint8(req.Threshold))
		if err != nil {
			return err
		}

		for _, b := range balances {
			resp.Balances = append(resp.Balances, &cowallet.Balance{
				AssetId:       b.AssetID,
				Amount:        b.Amount.String(),
				UnspentAmount: b.UnspentAmount.String(),
				SignedAmount:  b.SignedAmount.String(),
			})
		}

		for _, tx := range transactions {
			resp.Transactions = append(resp.Transactions, &cowallet.Transaction{
				AssetId:   tx.AssetID,
				Amount:    tx.Amount.String(),
				Hash:      tx.Hash,
				Tx:        tx.Tx,
				UpdatedAt: tx.UpdatedAt.Format(time.RFC3339Nano),
			})
		}

		return nil
	}); err != nil {
		slog.Error("rpc: group utxo failed", slog.Any("err", err))
		return nil, err
	}

	return resp, nil
}

func (s *Server) CreateTransfer(ctx context.Context, req *cowallet.CreateTransferRequest) (*cowallet.CreateTransferResponse, error) {
	user, ok := UserFrom(ctx)
	if !ok {
		return nil, twirp.Unauthenticated.Error("auth required")
	}

	if !govalidator.IsIn(user.MixinID, req.Members...) {
		return nil, twirp.PermissionDenied.Error("permission denied")
	}

	amount := g.Try(decimal.NewFromString(req.Amount)).Truncate(8)
	if !amount.IsPositive() {
		return nil, twirp.InvalidArgument.Error("invalid amount")
	}

	txn := s.db.NewTransaction(false)
	outputs, err := listUnspent(txn, req.Members, uint8(req.Threshold), req.AssetId, amount)
	txn.Discard()

	if err != nil {
		return nil, err
	}

	input := &mixin.TransactionInput{
		Memo: req.Memo,
		Hint: req.TraceId,
	}

	for _, output := range outputs {
		input.AppendUTXO(output)
	}

	if input.TotalInputAmount().LessThan(amount) {
		return nil, twirp.InvalidArgument.Error("insufficient balance")
	}

	input.AppendOutput(req.Receivers, uint8(req.ReceiverThreshold), amount)
	tx, err := mixin.NewFromAccessToken(user.Token).MakeMultisigTransaction(ctx, input)
	if err != nil {
		slog.Error("rpc: make multisig transaction failed", slog.Any("err", err))
		return nil, err
	}

	resp := &cowallet.CreateTransferResponse{
		Transaction: &cowallet.Transaction{
			AssetId:   req.AssetId,
			Amount:    amount.String(),
			Hash:      g.Must(tx.TransactionHash()).String(),
			Tx:        g.Must(tx.DumpTransaction()),
			UpdatedAt: time.Now().Format(time.RFC3339Nano),
		},
	}

	return resp, nil
}

func (s *Server) ListTransactions(ctx context.Context, req *cowallet.ListTransactionsRequest) (*cowallet.ListTransactionsResponse, error) {
	user, ok := UserFrom(ctx)
	if !ok {
		return nil, twirp.Unauthenticated.Error("auth required")
	}

	if !govalidator.IsIn(user.MixinID, req.Members...) {
		return nil, twirp.PermissionDenied.Error("permission denied")
	}

	since := cast.ToTime(req.Since)
	if since.IsZero() {
		since = time.Now()
	}

	limit := int(req.Limit)
	if limit == 0 || limit > 500 {
		limit = 500
	}

	resp := &cowallet.ListTransactionsResponse{}

	txn := s.db.NewTransaction(false)
	defer txn.Discard()

	set := mapset.New[string]()

	for {
		txs, err := listTransactions(txn, req.Members, uint8(req.Threshold), since, limit)
		if err != nil {
			slog.Error("rpc: list transactions failed", slog.Any("err", err))
			return nil, err
		}

		for _, tx := range txs {
			since = tx.UpdatedAt

			if set.Has(tx.Hash) {
				continue
			}

			if req.AssetId != "" && req.AssetId != tx.AssetID {
				continue
			}

			resp.Transactions = append(resp.Transactions, &cowallet.Transaction{
				AssetId:   tx.AssetID,
				Amount:    tx.Amount.String(),
				Hash:      tx.Hash,
				Tx:        tx.Tx,
				UpdatedAt: tx.UpdatedAt.Format(time.RFC3339Nano),
			})

			set.Put(tx.Hash)
		}

		if len(resp.Transactions) >= limit || len(txs) < limit {
			break
		}
	}

	return resp, nil
}
