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
	vaultPrefix                   = []byte("v:")
	jobPrefix                     = []byte("j:")
	snapshotPrefix                = []byte("s:")
	snapshotVaultIndexPrefix      = []byte("sv:")
	snapshotVaultAssetIndexPrefix = []byte("sva:")
	propertyPrefix                = []byte("p:")
	logPrefix                     = []byte("l:")
	addressPrefix                 = []byte("a:")
	renewPrefix                   = []byte("r:")
	renewVaultIndexPrefix         = []byte("rv:")
)

func hashMembers(ids []string, threshold uint8) uuid.UUID {
	sort.Strings(ids)
	var in string
	for _, id := range ids {
		in = in + id
	}

	b := buildIndexKey(vaultPrefix, mixinnet.NewHash([]byte(in)), threshold)
	return uuid.NewSHA1(uuid.NameSpaceOID, b)
}

func saveSnapshot(txn *badger.Txn, s *Snapshot, members []string, threshold uint8) error {
	pk := buildIndexKey(snapshotPrefix, s.ID)

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
			snapshotVaultIndexPrefix,
			hashMembers(members, threshold),
			s.CreatedAt.UnixNano(),
			s.ID,
		)

		if err := txn.Set(key, nil); err != nil {
			return err
		}
	}

	// index with asset
	{
		key := buildIndexKey(
			snapshotVaultAssetIndexPrefix,
			hashMembers(members, threshold),
			uuid.MustParse(s.AssetID),
			s.CreatedAt.UnixNano(),
			s.ID,
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

	prefix := snapshotVaultIndexPrefix
	args := []any{hashMembers(members, threshold)}

	if assetID != "" {
		asset, err := uuid.Parse(assetID)
		if err != nil {
			return nil, err
		}

		prefix = snapshotVaultAssetIndexPrefix
		args = append(args, asset)
	}

	prefix = buildIndexKey(prefix, args...)

	ts := offset.UnixNano()
	if ts <= 0 {
		ts = time.Now().UnixNano()
	}

	it.Seek(buildIndexKey(prefix, ts))
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
	b, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}

	pk := buildIndexKey(jobPrefix, hashMembers(job.Members, job.Threshold))
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
	b, err := json.Marshal(vault)
	if err != nil {
		panic(err)
	}

	pk := buildIndexKey(vaultPrefix, hashMembers(vault.Members, vault.Threshold))
	e := badger.NewEntry(pk, b)
	return txn.SetEntry(e)
}

func findVault(txn *badger.Txn, members []string, threshold uint8) (*Vault, error) {
	pk := buildIndexKey(vaultPrefix, hashMembers(members, threshold))

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

// func SaveVault(db *badger.DB, vault *Vault) error {
// 	txn := db.NewTransaction(true)
// 	defer txn.Discard()

// 	if err := saveVault(txn, vault); err != nil {
// 		return err
// 	}

// 	return txn.Commit()
// }

func readProperty(txn *badger.Txn, key string, val any) error {
	item, err := txn.Get(buildIndexKey(propertyPrefix, key))
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
	}

	return item.Value(func(b []byte) error {
		return json.Unmarshal(b, val)
	})
}

func ReadProperty(db *badger.DB, key string, val any) error {
	txn := db.NewTransaction(false)
	defer txn.Discard()

	return readProperty(txn, key, val)
}

func saveProperty(txn *badger.Txn, key string, val any) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}

	return txn.Set(buildIndexKey(propertyPrefix, key), b)
}

func SaveProperty(db *badger.DB, key string, val any) error {
	txn := db.NewTransaction(true)
	defer txn.Discard()

	return saveProperty(txn, key, val)
}

func saveLog(txn *badger.Txn, log *Log) error {
	b, err := json.Marshal(log)
	if err != nil {
		return err
	}

	key := buildIndexKey(logPrefix, log.Seq)
	return txn.Set(key, b)
}

func deleteLog(txn *badger.Txn, seq uint64) error {
	key := buildIndexKey(logPrefix, seq)
	return txn.Delete(key)
}

func listLogs(txn *badger.Txn, limit int) ([]*Log, error) {
	opts := badger.DefaultIteratorOptions
	opts.PrefetchSize = limit
	it := txn.NewIterator(opts)
	defer it.Close()

	var logs []*Log
	for it.Seek(logPrefix); it.ValidForPrefix(logPrefix) && len(logs) < limit; it.Next() {
		var log Log

		if err := it.Item().Value(func(b []byte) error {
			return json.Unmarshal(b, &log)
		}); err != nil {
			return nil, err
		}

		logs = append(logs, &log)
	}

	return logs, nil
}

func ListLogs(db *badger.DB, limit int) ([]*Log, error) {
	txn := db.NewTransaction(false)
	defer txn.Discard()

	return listLogs(txn, limit)
}

func saveRenew(txn *badger.Txn, r *Renew) error {
	pk := buildIndexKey(renewPrefix, r.ID)

	b, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}

	if err := txn.Set(pk, b); err != nil {
		return err
	}

	// index
	{
		key := buildIndexKey(
			renewVaultIndexPrefix,
			hashMembers(r.Members, r.Threshold),
			r.CreatedAt.UnixNano(),
			r.ID,
		)

		if err := txn.Set(key, nil); err != nil {
			return err
		}
	}

	return nil
}

func findRenew(txn *badger.Txn, id uuid.UUID) (*Renew, error) {
	pk := buildIndexKey(renewPrefix, id)
	item, err := txn.Get(pk)
	if err != nil {
		return nil, err
	}

	b, err := item.ValueCopy(nil)
	if err != nil {
		return nil, err
	}

	var r Renew
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}

	return &r, nil
}

func saveAddress(txn *badger.Txn, v Address) error {
	v.UpdatedAt = time.Now()

	pk := buildIndexKey(addressPrefix, v.UserID, hashMembers(v.Members, v.Threshold))
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	if err := txn.Set(pk, b); err != nil {
		return err
	}

	return nil
}

func deleteAddress(txn *badger.Txn, user uuid.UUID, members []string, threshold uint8) error {
	pk := buildIndexKey(addressPrefix, user, hashMembers(members, threshold))
	return txn.Delete(pk)
}

func listAddress(txn *badger.Txn, user uuid.UUID) ([]Address, error) {
	opt := badger.DefaultIteratorOptions
	it := txn.NewIterator(opt)
	defer it.Close()

	var outputs []Address

	prefix := buildIndexKey(addressPrefix, user)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		var v Address
		if err := it.Item().Value(func(b []byte) error {
			return json.Unmarshal(b, &v)
		}); err != nil {
			return nil, err
		}

		outputs = append(outputs, v)
	}

	return outputs, nil
}

func ListAddress(db *badger.DB, user uuid.UUID) ([]Address, error) {
	txn := db.NewTransaction(false)
	defer txn.Discard()

	return listAddress(txn, user)
}
