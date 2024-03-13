package cowallet

import (
	"context"
	"log/slog"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
)

type Job struct {
	CreatedAt time.Time `json:"created_at"`
	User      *User     `json:"user"`
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
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
		a, b          uint64
		offset        = vault.Offset
		assets        = map[string]*Asset{}
		spentRequests = map[string]*mixin.SafeMultisigRequest{}
	)

	for {
		outputs, err := client.SafeListUtxos(ctx, mixin.SafeListUtxoOption{
			Members:   vault.Members,
			Threshold: vault.Threshold,
			Offset:    offset,
			Limit:     500,
		})

		if err != nil {
			slog.Error("SafeListUtxos", "error", err)
			return err
		}

		for _, output := range outputs {
			offset = output.Sequence + 1

			if output.State == mixin.SafeUtxoStateSpent {
				if _, ok := spentRequests[output.SignedBy]; !ok {
					req, err := client.SafeReadMultisigRequests(ctx, output.SignedBy)
					if err != nil {
						slog.Error("SafeReadMultisigRequests", "error", err)
						return err
					}

					spentRequests[output.SignedBy] = req
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

	vault.Offset = getNewOffset(a, b)
	vault.UpdatedAt = time.Now()

	txn := db.NewTransaction(true)
	defer txn.Discard()

	for _, req := range spentRequests {
		if err := saveRequest(txn, req); err != nil {
			slog.Error("saveRequest", "error", err)
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
