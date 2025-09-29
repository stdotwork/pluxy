package conversation

import (
	"bytes"
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
		// Namespace by account label to avoid cross-account collisions.
		label := strings.ToLower(strings.TrimSpace(record.AccountLabel))
		if label == "" {
			return errors.New("gemini-web conversation: empty account label")
		}
		key := []byte(hash + ":" + label)
		if err := bucket.Put(key, payload); err != nil {
			return err
		}
		// Best-effort cleanup of legacy single-key format (hash -> MatchRecord).
		// We do not know its label; leave it for lookup fallback/cleanup elsewhere.
		return nil
	})
}

// LookupMatch retrieves a stored mapping.
// It prefers namespaced entries (hash:label). If multiple labels exist for the same
// hash, it returns not found to avoid redirecting to the wrong credential.
// Falls back to legacy single-key entries if present.
func LookupMatch(hash string) (MatchRecord, bool, error) {
	db, err := openIndex()
	if err != nil {
		return MatchRecord{}, false, err
	}
	var foundOne bool
	var ambiguous bool
	var firstLabel string
	var single MatchRecord
	err = db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketMatches))
		if bucket == nil {
			return nil
		}
		// Scan namespaced keys with prefix "hash:"
		prefix := []byte(hash + ":")
		c := bucket.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if len(v) == 0 {
				continue
			}
			var rec MatchRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				// Ignore malformed; removal is handled elsewhere.
				continue
			}
			if strings.TrimSpace(rec.AccountLabel) == "" || rec.PrefixLen <= 0 {
				continue
			}
			label := strings.ToLower(strings.TrimSpace(rec.AccountLabel))
			if !foundOne {
				firstLabel = label
				single = rec
				foundOne = true
				continue
			}
			if label != firstLabel {
				ambiguous = true
				// Early exit scan; ambiguity detected.
				return nil
			}
		}
		if foundOne {
			return nil
		}
		// Fallback to legacy single-key format
		raw := bucket.Get([]byte(hash))
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &single)
	})
	if err != nil {
		return MatchRecord{}, false, err
	}
	if ambiguous {
		return MatchRecord{}, false, nil
	}
	if strings.TrimSpace(single.AccountLabel) == "" || single.PrefixLen <= 0 {
		return MatchRecord{}, false, nil
	}
	return single, true, nil
}

// RemoveMatch deletes all mappings for the given hash (all labels and legacy key).
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
		// Delete namespaced entries
		prefix := []byte(hash + ":")
		c := bucket.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			if err := bucket.Delete(k); err != nil {
				return err
			}
		}
		// Delete legacy entry
		_ = bucket.Delete([]byte(hash))
		return nil
	})
}

// RemoveMatchForLabel deletes the mapping for the given hash and label only.
func RemoveMatchForLabel(hash, label string) error {
	label = strings.ToLower(strings.TrimSpace(label))
	if strings.TrimSpace(hash) == "" || label == "" {
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
		// Remove namespaced key
		_ = bucket.Delete([]byte(hash + ":" + label))
		// If legacy single-key exists and matches label, remove it as well.
		if raw := bucket.Get([]byte(hash)); len(raw) > 0 {
			var rec MatchRecord
			if err := json.Unmarshal(raw, &rec); err == nil {
				if strings.EqualFold(strings.TrimSpace(rec.AccountLabel), label) {
					_ = bucket.Delete([]byte(hash))
				}
			}
		}
		return nil
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
