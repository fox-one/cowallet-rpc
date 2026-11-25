package cowallet

import (
	"context"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/fox-one/mixin-sdk-go/v2/mixinnet"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	SpendKey   mixinnet.Key
	PayAssetID string
	PayAmount  decimal.Decimal
	Whitelist  []string
}

type Server struct {
	db     *badger.DB
	client *mixin.Client
	cfg    Config

	assets    *cache.Cache[string, *mixin.SafeAsset]
	whitelist map[string]struct{}
}

func NewServer(
	db *badger.DB,
	client *mixin.Client,
	cfg Config,
) Server {
	whitelist := make(map[string]struct{}, len(cfg.Whitelist))
	for _, item := range cfg.Whitelist {
		id := strings.TrimSpace(item)
		if id == "" {
			continue
		}
		if uid, err := uuid.Parse(id); err == nil {
			id = uid.String()
		}
		whitelist[id] = struct{}{}
	}

	return Server{
		db:        db,
		client:    client,
		cfg:       cfg,
		assets:    cache.New[string, *mixin.SafeAsset](),
		whitelist: whitelist,
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
