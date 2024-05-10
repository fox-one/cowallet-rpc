package cowallet

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2/mixinnet"
	"github.com/google/uuid"
)

var (
	vaultPrefix    = []byte("v:")
	jobPrefix      = []byte("j:")
	snapshotPrefix = []byte("s:")
)

func hashMembers(ids []string) mixinnet.Hash {
	sort.Strings(ids)
	var in string
	for _, id := range ids {
		in = in + id
	}

	return mixinnet.NewHash([]byte(in))
}

func saveSnapshot(txn *badger.Txn, s *Snapshot, members []string, threshold uint8) error {
	pk := buildIndexKey(snapshotPrefix, uuid.MustParse(s.ID))

	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}

	if err := txn.Set(pk, b); err != nil {
		return err
	}

	// index 1
	{
		key := buildIndexKey(
			snapshotPrefix,
			hashMembers(members),
			threshold,
			s.CreatedAt.UnixNano(),
			uuid.MustParse(s.ID),
		)

		if err := txn.Set(key, nil); err != nil {
			return err
		}
	}

	// index with asset
	{
		key := buildIndexKey(
			snapshotPrefix,
			hashMembers(members),
			threshold,
			uuid.MustParse(s.AssetID),
			s.CreatedAt.UnixNano(),
			uuid.MustParse(s.ID),
		)

		if err := txn.Set(key, nil); err != nil {
			return err
		}
	}

	return nil
}

func findSnapshot(txn *badger.Txn, id uuid.UUID) (*Snapshot, error) {
	key := buildIndexKey(snapshotPrefix, id)
	item, err := txn.Get(key)
	if err != nil {
		return nil, err
	}

	var s Snapshot
	if err := item.Value(func(b []byte) error {
		return json.Unmarshal(b, &s)
	}); err != nil {
		return nil, err
	}

	return &s, nil
}

func listSnapshots(txn *badger.Txn, members []string, threshold uint8, assetID string, offset time.Time, limit int) ([]*Snapshot, error) {
	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = limit
	opts.Reverse = true

	it := txn.NewIterator(opts)
	defer it.Close()

	prefix := buildIndexKey(
		snapshotPrefix,
		hashMembers(members),
		threshold,
	)

	if assetID != "" {
		asset, err := uuid.Parse(assetID)
		if err != nil {
			return nil, err
		}

		prefix = buildIndexKey(prefix, asset)
	}

	ts := offset.UnixNano()
	if ts > 0 {
		it.Seek(buildIndexKey(prefix, ts))
	} else {
		it.Seek(prefix)
	}

	snapshots := []*Snapshot{}
	for ; it.ValidForPrefix(prefix) && len(snapshots) < limit; it.Next() {
		key := it.Item().Key()

		var id uuid.UUID
		decodeIndexKey(key, prefix, &ts, &id)

		s, err := findSnapshot(txn, id)
		if err != nil {
			return nil, err
		}

		snapshots = append(snapshots, s)
	}

	return snapshots, nil
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
