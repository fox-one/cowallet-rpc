package cowallet

import (
	"context"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	db *badger.DB
}

func NewServer(db *badger.DB) Server {
	return Server{db: db}
}

func (s *Server) Run(ctx context.Context) error {
	dur := time.Minute

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
