package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/dgraph-io/badger/v4"
	backend "github.com/fox-one/cowallet"
	"golang.org/x/sync/errgroup"
)

var cfg struct {
	dbPath string
	port   int
}

func init() {
	flag.StringVar(&cfg.dbPath, "db", "cowallet.db", "database path")
	flag.IntVar(&cfg.port, "port", 8080, "http port")

	flag.Parse()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()

	db, err := badger.Open(badger.DefaultOptions(cfg.dbPath))
	if err != nil {
		slog.Error("open db failed", slog.Any("err", err))
		return
	}

	slog.Info("cowallet rpc launch", "ver", "0.01")

	svr := backend.NewServer(db)

	s := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.port),
		Handler: svr.Handler(),
	}

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		slog.Info("http listen", slog.String("addr", s.Addr))
		return s.ListenAndServe()
	})

	g.Go(func() error {
		<-ctx.Done()

		return s.Shutdown(ctx)
	})

	g.Go(func() error {
		return runGC(ctx, db, time.Minute)
	})

	g.Go(func() error {
		return svr.Run(ctx)
	})

	_ = g.Wait()
}

func runGC(ctx context.Context, db *badger.DB, dur time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dur):
			_ = db.RunValueLogGC(0.7)
		}
	}
}
