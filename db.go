package cowallet

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/fox-one/mixin-sdk-go/v2/mixinnet"
	"github.com/google/uuid"
	g "github.com/pandodao/generic"
)

var (
	requestPrefix = []byte("r:")
	vaultPrefix   = []byte("v:")
	jobPrefix     = []byte("j:")
)

func hashMembers(ids []string) mixinnet.Hash {
	sort.Strings(ids)
	var in string
	for _, id := range ids {
		in = in + id
	}

	return mixinnet.NewHash([]byte(in))
}

// saveRequest 保存已经 spent 的 multisiq request
func saveRequest(txn *badger.Txn, req *mixin.SafeMultisigRequest) error {
	pk := buildIndexKey(
		requestPrefix,
		hashMembers(req.Senders),
		req.SendersThreshold,
		req.CreatedAt.UnixNano(),
		uuid.MustParse(req.RequestID),
	)

	e := badger.NewEntry(pk, g.Must(json.Marshal(req)))
	return txn.SetEntry(e)
}

func listRequests(txn *badger.Txn, members []string, threshold uint8, since time.Time, limit int) ([]*mixin.SafeMultisigRequest, error) {
	prefix := buildIndexKey(
		requestPrefix,
		hashMembers(members),
		threshold,
	)

	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = limit
	opts.Reverse = true
	it := txn.NewIterator(opts)
	defer it.Close()

	if since.IsZero() {
		it.Seek(prefix)
	} else {
		it.Seek(buildIndexKey(prefix, since.UnixNano()))
	}

	var requests []*mixin.SafeMultisigRequest
	for ; it.ValidForPrefix(prefix) && len(requests) < limit; it.Next() {
		item := it.Item()

		var req mixin.SafeMultisigRequest
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &req)
		}); err != nil {
			return nil, err
		}

		requests = append(requests, &req)
	}

	return requests, nil
}

func saveJob(txn *badger.Txn, job *Job, ttl time.Duration) error {
	pk := buildIndexKey(
		jobPrefix,
		hashMembers(job.Members),
		job.Threshold,
	)

	b, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}

	e := badger.NewEntry(pk, b).WithTTL(ttl)
	return txn.SetEntry(e)
}

func listJobs(txn *badger.Txn) ([]*Job, error) {
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

func ListJobs(db *badger.DB) ([]*Job, error) {
	txn := db.NewTransaction(false)
	defer txn.Discard()

	return listJobs(txn)
}

func saveVault(txn *badger.Txn, vault *Vault) error {
	pk := buildIndexKey(
		vaultPrefix,
		hashMembers(vault.Members),
		vault.Threshold,
	)

	b, err := json.Marshal(vault)
	if err != nil {
		panic(err)
	}

	e := badger.NewEntry(pk, b)
	return txn.SetEntry(e)
}

func findVault(txn *badger.Txn, members []string, threshold uint8) (*Vault, error) {
	pk := buildIndexKey(
		vaultPrefix,
		hashMembers(members),
		threshold,
	)

	item, err := txn.Get(pk)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return &Vault{
				Members:   members,
				Threshold: threshold,
			}, nil
		}

		return nil, err
	}

	var vault Vault
	if err := item.Value(func(val []byte) error {
		return json.Unmarshal(val, &vault)
	}); err != nil {
		return nil, err
	}

	return &vault, nil
}

func FindVault(db *badger.DB, members []string, threshold uint8) (*Vault, error) {
	txn := db.NewTransaction(false)
	defer txn.Discard()

	return findVault(txn, members, threshold)
}

func SaveVault(db *badger.DB, vault *Vault) error {
	txn := db.NewTransaction(true)
	defer txn.Discard()

	if err := saveVault(txn, vault); err != nil {
		return err
	}

	return txn.Commit()
}
