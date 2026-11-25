package cowallet

import (
	"context"
	"encoding/hex"
	"fmt"
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

var (
	safeListUtxos = func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		return client.SafeListUtxos(ctx, opt)
	}
	safeReadMultisigRequests = func(ctx context.Context, client *mixin.Client, id string) (*mixin.SafeMultisigRequest, error) {
		return client.SafeReadMultisigRequests(ctx, id)
	}
	safeReadUtxoByHash = func(ctx context.Context, client *mixin.Client, hash mixinnet.Hash, index uint8) (*mixin.SafeUtxo, error) {
		return client.SafeReadUtxoByHash(ctx, hash, index)
	}
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
			return s.handleJob(ctx, job)
		})
	}

	return g.Wait()
}

func (s *Server) deleteJobIfTokenMatch(members []string, threshold uint8, token string) {
	if err := s.db.Update(func(txn *badger.Txn) error {
		return deleteJobIfTokenMatch(txn, members, threshold, token)
	}); err != nil {
		slog.Error("deleteJobIfTokenMatch", "members", members, "threshold", threshold, "err", err)
	}
}

func (s *Server) handleJobClientError(job *Job, err error) {
	if job.User == nil {
		return
	}

	if isPermanentAuthError(err) {
		s.invalidateToken(job.User.Token)
		s.deleteJobIfTokenMatch(job.Members, job.Threshold, job.User.Token)
		return
	}

	if isForbiddenError(err) {
		s.deleteJobIfTokenMatch(job.Members, job.Threshold, job.User.Token)
	}
}

func (s *Server) handleJob(ctx context.Context, job *Job) (err error) {
	if job.User == nil {
		return s.db.Update(func(txn *badger.Txn) error {
			if err := saveVaultIfNotExist(txn, &Vault{
				Members:   job.Members,
				Threshold: job.Threshold,
			}); err != nil {
				return err
			}

			return deleteJobIfNilUser(txn, job.Members, job.Threshold)
		})
	}

	if invalid, err := s.isTokenInvalid(job.User.Token); err == nil && invalid {
		slog.Warn("skip job with invalid token", "user", job.User.MixinID, "members", job.Members, "threshold", job.Threshold)
		s.deleteJobIfTokenMatch(job.Members, job.Threshold, job.User.Token)
		return nil
	}

	slog.Info("handle job", "user", job.User.MixinID, "members", job.Members, "threshold", job.Threshold)

	client, err := clientFromToken(job.User.Token)
	if err != nil {
		slog.Error("clientFromToken", "error", err)
		s.invalidateToken(job.User.Token)
		s.deleteJobIfTokenMatch(job.Members, job.Threshold, job.User.Token)
		return err
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("handleJob panic", "user", job.User.MixinID, "err", r)
			err = fmt.Errorf("%w: %v", errAuthPanic, r)
			s.handleJobClientError(job, err)
		}
	}()

	vault, err := FindVault(s.db, job.Members, job.Threshold)
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
		outputs, err := safeListUtxos(ctx, client, mixin.SafeListUtxoOption{
			Members:   vault.Members,
			Threshold: vault.Threshold,
			Offset:    offset,
			Limit:     limit,
		})

		if err != nil {
			slog.Error("SafeListUtxos", "error", err)
			s.handleJobClientError(job, err)
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
					req, err := safeReadMultisigRequests(ctx, client, output.SignedBy)
					if err != nil {
						slog.Error("SafeReadMultisigRequests", "error", err)
						s.handleJobClientError(job, err)
						return err
					}

					if req.Amount.IsZero() {
						h, _ := mixinnet.HashFromString(req.TransactionHash)
						utxo, err := safeReadUtxoByHash(ctx, client, h, 0)
						if err != nil {
							slog.Error("SafeReadUtxoByHash", "error", err)
							s.handleJobClientError(job, err)
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

	if offset <= vault.Offset && time.Since(vault.UpdatedAt) < time.Minute {
		return nil
	}

	vault.Assets = vault.Assets[0:0]
	for _, asset := range assets {
		vault.Assets = append(vault.Assets, asset)
	}

	vault.Offset = max(getNewOffset(a, b), vault.Offset)
	vault.UpdatedAt = time.Now()

	txn := s.db.NewTransaction(true)
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
