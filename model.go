package cowallet

import (
	"time"

	"github.com/google/uuid"
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
	ID              uuid.UUID       `json:"id"`
	CreatedAt       time.Time       `json:"created_at"`
	AssetID         string          `json:"asset_id"`
	Amount          decimal.Decimal `json:"amount"`
	Opponent        string          `json:"opponent"`
	Memo            string          `json:"memo"`
	TransactionHash string          `json:"transaction_hash"`
	OutputIndex     uint8           `json:"output_index"`
}

type Address struct {
	UserID    uuid.UUID `json:"user_id"`
	Members   []string  `json:"members"`
	Threshold uint8     `json:"threshold"`
	Label     string    `json:"label"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Renew struct {
	ID        uuid.UUID       `json:"id"`
	Sequence  uint64          `json:"sequence"`
	CreatedAt time.Time       `json:"created_at"`
	Members   []string        `json:"members"`
	Threshold uint8           `json:"threshold"`
	Sender    string          `json:"sender"`
	Asset     string          `json:"asset"`
	Amount    decimal.Decimal `json:"amount"`
	Period    int64           `json:"period"` // in seconds
	From      time.Time       `json:"from"`
	To        time.Time       `json:"to"`
}

type Log struct {
	Seq       uint64    `json:"seq"`
	CreatedAt time.Time `json:"created_at"`
	TraceID   string    `json:"trace_id"`
	Memo      string    `json:"memo"`
}
