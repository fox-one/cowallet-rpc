package cowallet

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/fox-one/mixin-sdk-go/v2/mixinnet"
	"github.com/google/uuid"
	"github.com/zyedidia/generic/mapset"
	"golang.org/x/sync/errgroup"
)

type Job struct {
	CreatedAt time.Time `json:"created_at"`
	User      *User     `json:"user"`
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
}

func (s *Server) HandlePendingJobs(ctx context.Context) error {
	for {
		_ = s.handlePendingJobs(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) handlePendingJobs(ctx context.Context) error {
	jobs, err := ListJobs(s.db)
	if err != nil {
		slog.Error("ListJobs", slog.Any("err", err))
		return err
	}

	var g errgroup.Group
	g.SetLimit(10)

	for idx := range jobs {
		job := jobs[idx]
		g.Go(func() error {
			return handleJob(ctx, s.db, job)
		})
	}

	return g.Wait()
}

func handleJob(ctx context.Context, db *badger.DB, job *Job) error {
	slog.Info("handle job", "user", job.User.MixinID, "members", job.Members, "threshold", job.Threshold)

	client, err := mixin.NewFromOauthKeystore(&job.User.Key)
	if err != nil {
		slog.Error("NewFromOauthKeystore", "error", err)
		return err
	}

	vault, err := FindVault(db, job.Members, job.Threshold)
	if err != nil {
		slog.Error("find vault", "error", err)
		return err
	}

	var (
		a, b            uint64
		offset          = vault.Offset
		assets          = map[string]*Asset{}
		snapshots       []*Snapshot
		handledSignedBy = mapset.New[string]()
	)

	addr := mixin.RequireNewMixAddress(job.Members, job.Threshold).String()

	for {
		const limit = 500
		outputs, err := client.SafeListUtxos(ctx, mixin.SafeListUtxoOption{
			Members:   vault.Members,
			Threshold: vault.Threshold,
			Offset:    offset,
			Limit:     limit,
		})

		if err != nil {
			slog.Error("SafeListUtxos", "error", err)
			return err
		}

		for _, output := range outputs {
			offset = output.Sequence + 1

			// 收款
			if ok := output.OutputIndex > 0 &&
				len(output.Senders) > 0 &&
				mixin.RequireNewMixAddress(output.Senders, output.SendersThreshold).String() == addr; !ok {
				snapshots = append(snapshots, outputToSnapshot(output))
			}

			if output.State == mixin.SafeUtxoStateSpent {
				if !handledSignedBy.Has(output.SignedBy) {
					req, err := client.SafeReadMultisigRequests(ctx, output.SignedBy)
					if err != nil {
						slog.Error("SafeReadMultisigRequests", "error", err)
						return err
					}

					if req.Amount.IsZero() {
						h, _ := mixinnet.HashFromString(req.TransactionHash)
						utxo, err := client.SafeReadUtxoByHash(ctx, h, 0)
						if err != nil {
							slog.Error("SafeReadUtxoByHash", "error", err)
							return err
						}

						req.Amount = utxo.Amount
					}

					snapshots = append(snapshots, requestToSnapshot(req))
					handledSignedBy.Put(output.SignedBy)
				}

				a = output.Sequence + 1
				continue
			}

			asset, ok := assets[output.AssetID]
			if !ok {
				asset = &Asset{
					ID:   output.AssetID,
					Hash: output.KernelAssetID.String(),
				}

				assets[output.AssetID] = asset
			}

			asset.Balance = asset.Balance.Add(output.Amount)
			if output.State == mixin.SafeUtxoStateUnspent {
				asset.Unspent = asset.Unspent.Add(output.Amount)
			} else if output.State == mixin.SafeUtxoStateSigned {
				asset.Signed = asset.Signed.Add(output.Amount)
				if !govalidator.IsIn(output.SignedBy, asset.Requests...) {
					asset.Requests = append(asset.Requests, output.SignedBy)
				}
			}

			if b == 0 {
				b = output.Sequence
			}
		}

		if len(outputs) == 0 {
			break
		}
	}

	if offset <= vault.Offset {
		return nil
	}

	vault.Assets = vault.Assets[0:0]
	for _, asset := range assets {
		vault.Assets = append(vault.Assets, asset)
	}

	vault.Offset = getNewOffset(a, b)
	vault.UpdatedAt = time.Now()

	txn := db.NewTransaction(true)
	defer txn.Discard()

	for _, s := range snapshots {
		if err := saveSnapshot(txn, s, job.Members, job.Threshold); err != nil {
			slog.Error("saveSnapshot", "error", err)
			return err
		}
	}

	if err := saveVault(txn, vault); err != nil {
		slog.Error("saveVault", "error", err)
		return err
	}

	return txn.Commit()
}

func getNewOffset(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return max(a, b)
	} else {
		return min(a, b)
	}
}

func outputToSnapshot(output *mixin.SafeUtxo) *Snapshot {
	s := &Snapshot{
		ID:              uuid.MustParse(output.OutputID),
		CreatedAt:       output.CreatedAt,
		AssetID:         output.AssetID,
		Amount:          output.Amount,
		TransactionHash: output.TransactionHash.String(),
		OutputIndex:     output.OutputIndex,
	}

	if b, err := hex.DecodeString(output.Extra); err == nil {
		s.Memo = string(b)
	}

	if len(output.Senders) > 0 {
		s.Opponent = mixin.RequireNewMixAddress(output.Senders, output.SendersThreshold).String()
	}

	return s
}

func requestToSnapshot(req *mixin.SafeMultisigRequest) *Snapshot {
	s := &Snapshot{
		ID:              uuid.MustParse(req.RequestID),
		CreatedAt:       req.CreatedAt,
		AssetID:         req.AssetID,
		Amount:          req.Amount.Neg(),
		TransactionHash: req.TransactionHash,
	}

	if b, err := hex.DecodeString(req.Extra); err == nil {
		s.Memo = string(b)
	}

	return s
}
