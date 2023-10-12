package cowallet

import (
	"time"

	"github.com/fox-one/mixin-sdk-go"
	g "github.com/pandodao/generic"
	"github.com/shopspring/decimal"
)

type Balance struct {
	AssetID       string          `json:"asset_id"`
	Amount        decimal.Decimal `json:"amount"`
	UnspentAmount decimal.Decimal `json:"unspent"`
	SignedAmount  decimal.Decimal `json:"signed"`
}

type Transaction struct {
	UpdatedAt time.Time       `json:"updated_at"`
	AssetID   string          `json:"asset_id"`
	Amount    decimal.Decimal `json:"amount"`
	Hash      string          `json:"hash"`
	Tx        string          `json:"tx"`
}

func TransactionFromUTXO(utxo *mixin.MultisigUTXO) *Transaction {
	tx := g.Must(mixin.TransactionFromRaw(utxo.SignedTx))

	var sum decimal.Decimal
	for _, out := range tx.Outputs {
		amount := g.Try(decimal.NewFromString(out.Amount.String()))
		sum = sum.Add(amount)
	}

	return &Transaction{
		UpdatedAt: utxo.UpdatedAt,
		AssetID:   utxo.AssetID,
		Amount:    sum,
		Hash:      utxo.SignedBy,
		Tx:        utxo.SignedTx,
	}
}
