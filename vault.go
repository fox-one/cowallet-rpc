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
	Name      string    `json:"name"`
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
	Offset    uint64    `json:"offset"`
	Assets    []*Asset  `json:"assets"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Snapshot struct {
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	AssetID   string          `json:"asset_id"`
	Amount    decimal.Decimal `json:"amount"`
	Opponent  string          `json:"opponent"`
	Memo      string          `json:"memo"`
}
