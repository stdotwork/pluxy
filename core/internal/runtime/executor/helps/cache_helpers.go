package helps

import (
	"context"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheMap stores prompt cache IDs keyed by model+user_id.
// Protected by codexCacheMu. Entries expire after 1 hour.
var (
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu  sync.RWMutex
)

// codexCacheCleanupInterval controls how often expired entries are purged.
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce ensures the background cleanup goroutine starts only once.
var codexCacheCleanupOnce sync.Once

// startCodexCacheCleanup launches a background goroutine that periodically
// removes expired entries from codexCacheMap to prevent memory leaks.
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
		}
	}()
}

// purgeExpiredCodexCache removes entries that have expired.
func purgeExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

// SetCodexCacheRequired stores a cache entry for request-time paths.
func SetCodexCacheRequired(ctx context.Context, key string, cache CodexCache) error {
	ttl := time.Until(cache.Expire)
	if ttl <= 0 {
		return nil
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		_, errSet := homekv.KVSetJSONRequired(ctx, key, cache, ttl)
		return errSet
	}
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
	return nil
}
