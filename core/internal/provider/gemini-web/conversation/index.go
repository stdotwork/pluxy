package conversation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	bucketMatches    = "matches"
	defaultIndexFile = "gemini-web-index.bolt"
)

// MatchRecord stores persisted mapping metadata for a conversation prefix.
type MatchRecord struct {
	AccountLabel string   `json:"account_label"`
	Metadata     []string `json:"metadata,omitempty"`
	PrefixLen    int      `json:"prefix_len"`
	UpdatedAt    int64    `json:"updated_at"`
}

// MatchResult combines a persisted record with the hash that produced it.
type MatchResult struct {
	Hash   string
	Record MatchRecord
	Model  string
}

var (
	indexOnce sync.Once
	indexDB   *bolt.DB
	indexErr  error
)

func openIndex() (*bolt.DB, error) {
	indexOnce.Do(func() {
		path := indexPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			indexErr = err
			return
		}
		db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
		if err != nil {
			indexErr = err
			return
		}
		indexDB = db
	})
	return indexDB, indexErr
}

func indexPath() string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	return filepath.Join(wd, "conv", defaultIndexFile)
}

// StoreMatch persists or updates a conversation hash mapping.
func StoreMatch(hash string, record MatchRecord) error {
	if strings.TrimSpace(hash) == "" {
		return errors.New("gemini-web conversation: empty hash")
	}
	db, err := openIndex()
	if err != nil {
		return err
	}
	record.UpdatedAt = time.Now().UTC().Unix()
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(bucketMatches))
		if err != nil {
			return err
		}
		return bucket.Put([]byte(hash), payload)
	})
}

// LookupMatch retrieves a stored mapping.
func LookupMatch(hash string) (MatchRecord, bool, error) {
	db, err := openIndex()
	if err != nil {
		return MatchRecord{}, false, err
	}
	var record MatchRecord
	err = db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketMatches))
		if bucket == nil {
			return nil
		}
		raw := bucket.Get([]byte(hash))
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &record)
	})
	if err != nil {
		return MatchRecord{}, false, err
	}
	if record.AccountLabel == "" || record.PrefixLen <= 0 {
		return MatchRecord{}, false, nil
	}
	return record, true, nil
}

// RemoveMatch deletes a mapping for the given hash.
func RemoveMatch(hash string) error {
	db, err := openIndex()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketMatches))
		if bucket == nil {
			return nil
		}
		return bucket.Delete([]byte(hash))
	})
}

// RemoveMatchesByLabel removes all entries associated with the specified label.
func RemoveMatchesByLabel(label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	db, err := openIndex()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketMatches))
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			if len(v) == 0 {
				continue
			}
			var record MatchRecord
			if err := json.Unmarshal(v, &record); err != nil {
				_ = bucket.Delete(k)
				continue
			}
			if strings.EqualFold(strings.TrimSpace(record.AccountLabel), label) {
				if err := bucket.Delete(k); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StoreConversation updates all hashes representing the provided conversation snapshot.
func StoreConversation(label, model string, msgs []Message, metadata []string) error {
	label = strings.TrimSpace(label)
	if label == "" || len(msgs) == 0 {
		return nil
	}
	hashes := BuildStorageHashes(model, msgs)
	if len(hashes) == 0 {
		return nil
	}
	for _, h := range hashes {
		rec := MatchRecord{
			AccountLabel: label,
			Metadata:     append([]string(nil), metadata...),
			PrefixLen:    h.PrefixLen,
		}
		if err := StoreMatch(h.Hash, rec); err != nil {
			return err
		}
	}
	return nil
}
