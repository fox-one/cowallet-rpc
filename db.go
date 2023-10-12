package cowallet

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go"
	"github.com/google/uuid"
	g "github.com/pandodao/generic"
	"github.com/pandodao/mtg/mtgpack"
	"github.com/shopspring/decimal"
)

func buildIndexKey(prefix []byte, values ...any) []byte {
	enc := mtgpack.NewEncoder()
	if _, err := enc.Write(prefix); err != nil {
		panic(err)
	}

	if err := enc.EncodeValues(values...); err != nil {
		panic(err)
	}

	return enc.Bytes()
}

var (
	utxoPrefix        = []byte("utxo:")
	transactionPrefix = []byte("tx:")
	offsetPrefix      = []byte("offset:")
	jobPrefix         = []byte("job:")
)

func hashMembers(ids []string) mixin.Hash {
	sort.Strings(ids)
	var in string
	for _, id := range ids {
		in = in + id
	}

	return mixin.NewHash([]byte(in))
}

func saveTransaction(txn *badger.Txn, utxo *mixin.MultisigUTXO) error {
	pk := buildIndexKey(
		transactionPrefix,
		hashMembers(utxo.Members),
		utxo.Threshold,
		utxo.UpdatedAt.UnixNano(),
		g.Must(mixin.HashFromString(utxo.SignedBy)),
	)

	tx := TransactionFromUTXO(utxo)
	b, err := json.Marshal(tx)
	if err != nil {
		panic(err)
	}

	return txn.Set(pk, b)
}

func listTransactions(txn *badger.Txn, members []string, threshold uint8, since time.Time, limit int) ([]*Transaction, error) {
	prefix := buildIndexKey(
		transactionPrefix,
		hashMembers(members),
		threshold,
	)

	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = limit
	opts.Reverse = true
	it := txn.NewIterator(opts)
	defer it.Close()

	var (
		txs []*Transaction
	)

	seekKey := prefix
	if !since.IsZero() {
		seekKey = buildIndexKey(prefix, since.UnixNano())
	}

	for it.Seek(seekKey); it.ValidForPrefix(prefix) && len(txs) < limit; it.Next() {
		item := it.Item()

		var tx Transaction
		err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &tx)
		})

		if err != nil {
			return nil, err
		}

		txs = append(txs, &tx)
	}

	return txs, nil
}

func saveUTXO(txn *badger.Txn, utxo *mixin.MultisigUTXO) error {
	pk := buildIndexKey(
		utxoPrefix,
		hashMembers(utxo.Members),
		utxo.Threshold,
		uuid.MustParse(utxo.AssetID),
		utxo.UpdatedAt.UnixNano(),
		uuid.MustParse(utxo.UTXOID),
	)

	if utxo.State == mixin.UTXOStateSpent {
		if err := saveTransaction(txn, utxo); err != nil {
			return err
		}

		return txn.Delete(pk)
	}

	b, err := json.Marshal(utxo)
	if err != nil {
		panic(err)
	}

	return txn.Set(pk, b)
}

func groupUTXO(txn *badger.Txn, members []string, threshold uint8) ([]*Balance, []*Transaction, error) {
	var (
		balances     = map[string]*Balance{}
		transactions = map[string]*Transaction{}
	)

	prefix := buildIndexKey(utxoPrefix, hashMembers(members), threshold)
	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = 100
	it := txn.NewIterator(opts)
	defer it.Close()

	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()

		var utxo mixin.MultisigUTXO
		err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &utxo)
		})

		if err != nil {
			return nil, nil, err
		}

		b, ok := balances[utxo.AssetID]
		if !ok {
			b = &Balance{
				AssetID: utxo.AssetID,
			}

			balances[utxo.AssetID] = b
		}

		b.Amount = b.Amount.Add(utxo.Amount)

		switch utxo.State {
		case mixin.UTXOStateUnspent:
			b.UnspentAmount = b.UnspentAmount.Add(utxo.Amount)
		case mixin.UTXOStateSigned:
			b.SignedAmount = b.SignedAmount.Add(utxo.Amount)
			transactions[utxo.SignedBy] = TransactionFromUTXO(&utxo)
		}
	}

	return mapValues(balances), mapValues(transactions), nil
}

func listUnspent(
	txn *badger.Txn,
	members []string,
	threshold uint8,
	assetID string,
	amount decimal.Decimal,
) ([]*mixin.MultisigUTXO, error) {
	prefix := buildIndexKey(
		utxoPrefix,
		hashMembers(members),
		threshold,
		uuid.MustParse(assetID),
	)

	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = 10
	it := txn.NewIterator(opts)
	defer it.Close()

	var (
		utxos []*mixin.MultisigUTXO
		sum   decimal.Decimal
	)

	for it.Seek(prefix); it.ValidForPrefix(prefix) && sum.LessThan(amount); it.Next() {
		item := it.Item()

		var utxo mixin.MultisigUTXO
		err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &utxo)
		})

		if err != nil {
			return nil, err
		}

		if utxo.State != mixin.UTXOStateUnspent {
			continue
		}

		utxos = append(utxos, &utxo)
		sum = sum.Add(utxo.Amount)
	}

	return utxos, nil
}

func saveOffset(txn *badger.Txn, members []string, threshold uint8, offset time.Time) error {
	pk := buildIndexKey(
		offsetPrefix,
		hashMembers(members),
		threshold,
	)

	b, err := json.Marshal(offset)
	if err != nil {
		panic(err)
	}

	return txn.Set(pk, b)
}

func getOffset(txn *badger.Txn, members []string, threshold uint8) (time.Time, error) {
	pk := buildIndexKey(
		offsetPrefix,
		hashMembers(members),
		threshold,
	)

	var offset time.Time
	b, err := txn.Get(pk)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return offset, nil
		}

		return offset, err
	}

	err = b.Value(func(val []byte) error {
		return json.Unmarshal(val, &offset)
	})

	return offset, err
}

func saveJob(txn *badger.Txn, job *Job) error {
	pk := buildIndexKey(
		jobPrefix,
		hashMembers(job.Members),
		job.Threshold,
	)

	b, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}

	return txn.Set(pk, b)
}

func deleteJob(txn *badger.Txn, job *Job) error {
	pk := buildIndexKey(
		jobPrefix,
		hashMembers(job.Members),
		job.Threshold,
	)

	return txn.Delete(pk)
}

func listJobs(txn *badger.Txn, limit int) ([]*Job, error) {
	var jobs []*Job

	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = 10
	it := txn.NewIterator(opts)
	defer it.Close()

	for it.Seek(jobPrefix); it.ValidForPrefix(jobPrefix); it.Next() {
		item := it.Item()

		var job Job
		err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &job)
		})

		if err != nil {
			return nil, err
		}

		jobs = append(jobs, &job)
	}

	return jobs, nil
}
