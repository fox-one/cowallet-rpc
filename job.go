package cowallet

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go"
)

type Job struct {
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token"`
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
}

func handleJob(ctx context.Context, db *badger.DB, job *Job) error {
	txn := db.NewTransaction(true)
	defer txn.Discard()

	offset, err := getOffset(txn, job.Members, job.Threshold)
	if err != nil {
		slog.Error("get offset failed", slog.Any("err", err))
		return err
	}

	slog.With(
		slog.String("members", strings.Join(job.Members, ",")),
		slog.Int("threshold", int(job.Threshold)),
	).Info("handle sync job", slog.Time("offset", offset))

	const limit = 500
	client := mixin.NewFromAccessToken(job.Token)

	for {
		outputs, err := client.ListMultisigOutputs(ctx, mixin.ListMultisigOutputsOption{
			Members:        job.Members,
			Threshold:      job.Threshold,
			Offset:         offset,
			Limit:          limit,
			OrderByCreated: false,
		})

		if err != nil {
			slog.Error("list multisig outputs failed", slog.Any("err", err))
			return err
		}

		for _, output := range outputs {
			offset = output.UpdatedAt

			if err := saveUTXO(txn, output); err != nil {
				slog.Error("save utxo failed", slog.Any("err", err))
				return err
			}
		}

		if len(outputs) < limit {
			break
		}
	}

	if err := saveOffset(txn, job.Members, job.Threshold, offset); err != nil {
		slog.Error("save offset failed", slog.Any("err", err))
		return err
	}

	slog.With(
		slog.String("members", strings.Join(job.Members, ",")),
		slog.Int("threshold", int(job.Threshold)),
	).Info("finish sync job", slog.Time("offset", offset))

	return txn.Commit()
}
