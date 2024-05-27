package cowallet

import (
	"context"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/fox-one/mixin-sdk-go/v2/mixinnet"
	"github.com/shopspring/decimal"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	SpendKey   mixinnet.Key
	PayAssetID string
	PayAmount  decimal.Decimal
}

type Server struct {
	db     *badger.DB
	client *mixin.Client
	cfg    Config

	assets *cache.Cache[string, *mixin.SafeAsset]
}

func NewServer(
	db *badger.DB,
	client *mixin.Client,
	cfg Config,
) Server {
	return Server{
		db:     db,
		client: client,
		cfg:    cfg,
		assets: cache.New[string, *mixin.SafeAsset](),
	}
}

func (s *Server) Run(ctx context.Context) error {
	var g errgroup.Group

	g.Go(func() error {
		return s.LoopOutputs(ctx)
	})

	g.Go(func() error {
		return s.HandlePendingJobs(ctx)
	})

	return g.Wait()
}
