package cowallet

import (
	"context"
	"log/slog"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/dgraph-io/badger/v4"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Issuer string `valid:"required"`
}

type Server struct {
	db  *badger.DB
	cfg Config
}

func NewServer(db *badger.DB, cfg Config) Server {
	if _, err := govalidator.ValidateStruct(cfg); err != nil {
		panic(err)
	}

	return Server{
		db:  db,
		cfg: cfg,
	}
}

func (s *Server) Run(ctx context.Context) error {
	dur := time.Millisecond

	for {
		_ = s.run(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dur):
		}
	}
}

func (s *Server) run(ctx context.Context) error {
	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	const limit = 10

	jobs, err := listJobs(txn, limit)
	if err != nil {
		slog.Error("list jobs failed", slog.Any("err", err))
		return err
	}

	var g errgroup.Group
	for idx := range jobs {
		job := jobs[idx]
		g.Go(func() error {
			return handleJob(ctx, s.db, job)
		})

		if err := deleteJob(txn, job); err != nil {
			slog.Error("delete job failed", slog.Any("err", err))
			return err
		}
	}

	_ = txn.Commit()
	return g.Wait()
}
