package cowallet

import (
	"time"

	"github.com/shopspring/decimal"
)

type Asset struct {
	ID       string          `json:"id"`
	Hash     string          `json:"hash"`
	Balance  decimal.Decimal `json:"balance"`
	Unspent  decimal.Decimal `json:"unspent"`
	Signed   decimal.Decimal `json:"signed"`
	Requests []string        `json:"requests"`
}

type Vault struct {
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
	Offset    uint64    `json:"offset"`
	Assets    []*Asset  `json:"assets"`
	UpdatedAt time.Time `json:"updated_at"`
}
