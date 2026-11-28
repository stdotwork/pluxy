package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/server"
)

func keyCall(c *Core, key, method, path string, body io.Reader) (int, []byte) {
	if body == nil {
		body = http.NoBody
	}
	req := httptest.NewRequest(method, ManagementPrefix+strings.TrimPrefix(path, "/"), body)
	req.Header.Set("Authorization", "Bearer "+key)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	c.Management().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func stageKey(t *testing.T, c *Core, key string) (value, config string) {
	t.Helper()
	code, b := keyCall(c, key, http.MethodPost, "/management-key/rotate/stage", nil)
	var r struct {
		Value  string `json:"value"`
		Config string `json:"config"`
	}
	if code != http.StatusOK || json.Unmarshal(b, &r) != nil || r.Value == "" {
		t.Fatalf("stage: %d %s", code, b)
	}
	return r.Value, r.Config
}

func commitKey(c *Core, key, value string) (int, []byte) {
	body, _ := json.Marshal(map[string]string{"value": value})
	return keyCall(c, key, http.MethodPost, "/management-key/rotate/commit", bytes.NewReader(body))
}

func keyWorks(c *Core, key string) bool {
	code, _ := keyCall(c, key, http.MethodGet, "/status", nil)
	return code == http.StatusOK
}

func TestManagementKeyTwoSteps(t *testing.T) {
	const oldKey = "fake-management-key"
	fileKey := func(t *testing.T, path string) string {
		t.Helper()
		cfg, err := server.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.ManagementKey
	}
	unchanged := func(t *testing.T, c *Core, path string, before []byte) {
		t.Helper()
		if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, before) {
			t.Errorf("the config file changed: %s (%v)", now, err)
		}
		if !keyWorks(c, oldKey) {
			t.Error("the old key no longer works")
		}
	}

	t.Run("stage changes nothing", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		value, config := stageKey(t, c, oldKey)
		if config != path || value == oldKey {
			t.Errorf("stage named config %q, want %q; value %q", config, path, value)
		}
		unchanged(t, c, path, before)
		if keyWorks(c, value) {
			t.Error("the staged key works before it is committed")
		}
	})

	t.Run("commit puts the staged key into use", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		value, _ := stageKey(t, c, oldKey)
		if code, b := commitKey(c, oldKey, value); code != http.StatusNoContent {
			t.Fatalf("commit: %d %s", code, b)
		}
		if got := fileKey(t, path); got != value {
			t.Errorf("the config file holds %q, want the staged key", got)
		}
		if keyWorks(c, oldKey) || !keyWorks(c, value) {
			t.Errorf("after the commit: old key works %v, new key works %v", keyWorks(c, oldKey), keyWorks(c, value))
		}
	})

	t.Run("a wrong or missing value answers 409", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		stageKey(t, c, oldKey)
		for name, body := range map[string]string{"wrong": `{"value": "fake-not-the-staged-key"}`, "empty": `{"value": ""}`, "missing": `{}`} {
			if code, b := keyCall(c, oldKey, http.MethodPost, "/management-key/rotate/commit", strings.NewReader(body)); code != http.StatusConflict {
				t.Errorf("%s value: %d %s, want 409", name, code, b)
			}
		}
		unchanged(t, c, path, before)
	})

	t.Run("nothing staged after a restart", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		value, _ := stageKey(t, c, oldKey)
		c.Stop()
		c, err := Start(Options{ConfigPath: path, SafeMode: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Stop)
		if code, b := commitKey(c, oldKey, value); code != http.StatusConflict {
			t.Errorf("commit after a restart: %d %s, want 409", code, b)
		}
		unchanged(t, c, path, before)
	})

	t.Run("the key in use answers 204 and changes nothing", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		if code, b := commitKey(c, oldKey, oldKey); code != http.StatusNoContent {
			t.Errorf("commit of the key in use: %d %s, want 204", code, b)
		}
		unchanged(t, c, path, before)
	})

	t.Run("a second stage replaces the first", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		first, _ := stageKey(t, c, oldKey)
		second, _ := stageKey(t, c, oldKey)
		if code, b := commitKey(c, oldKey, first); code != http.StatusConflict {
			t.Errorf("commit of the replaced key: %d %s, want 409", code, b)
		}
		unchanged(t, c, path, before)
		if code, b := commitKey(c, oldKey, second); code != http.StatusNoContent || fileKey(t, path) != second {
			t.Errorf("commit of the second key: %d %s", code, b)
		}
	})

	t.Run("a staged key is gone after 10 minutes", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		before, _ := os.ReadFile(path)
		now := time.Now()
		c.keyNow = func() time.Time { return now }
		value, _ := stageKey(t, c, oldKey)
		now = now.Add(10 * time.Minute)
		if code, b := commitKey(c, oldKey, value); code != http.StatusConflict {
			t.Errorf("commit 10 minutes after the stage: %d %s, want 409", code, b)
		}
		unchanged(t, c, path, before)
		value, _ = stageKey(t, c, oldKey)
		now = now.Add(10*time.Minute - time.Second)
		if code, b := commitKey(c, oldKey, value); code != http.StatusNoContent || fileKey(t, path) != value {
			t.Errorf("commit within 10 minutes of the stage: %d %s", code, b)
		}
	})

	t.Run("a commit sent again while the first waits answers 204", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		value, _ := stageKey(t, c, oldKey)
		held, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			written <- c.WriteConfig("test: hold the lock", func(cur []byte) ([]byte, error) {
				close(held)
				<-release
				return nil, errors.New("test: nothing to write")
			})
		}()
		<-held
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range codes {
			read := make(chan struct{})
			body, _ := json.Marshal(map[string]string{"value": value})
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = keyCall(c, oldKey, http.MethodPost, "/management-key/rotate/commit", &readSignal{r: bytes.NewReader(body), done: read})
			}()
			<-read
		}
		close(release)
		wg.Wait()
		<-written
		if codes[0] != http.StatusNoContent || codes[1] != http.StatusNoContent {
			t.Errorf("the two commits answered %v, want 204 and 204", codes)
		}
		if fileKey(t, path) != value || !keyWorks(c, value) {
			t.Error("the staged key is not in use")
		}
	})

	t.Run("a stage waits for a commit between its check and its write", func(t *testing.T) {
		c, path := safeCore(t, safeConfig)
		first, _ := stageKey(t, c, oldKey)
		waits := make(chan struct{})
		c.staged.waiting = func() { close(waits) }
		second := make(chan string, 1)
		c.staged.checked = func() {
			c.staged.checked = nil
			go func() {
				code, b := keyCall(c, oldKey, http.MethodPost, "/management-key/rotate/stage", nil)
				var r struct {
					Value string `json:"value"`
				}
				if code != http.StatusOK || json.Unmarshal(b, &r) != nil || r.Value == "" {
					t.Errorf("the second stage: %d %s", code, b)
				}
				second <- r.Value
			}()
			select {
			case <-waits:
			case v := <-second:
				t.Errorf("a stage answered between the commit's check and its write")
				second <- v
			}
		}
		if code, b := commitKey(c, oldKey, first); code != http.StatusNoContent {
			t.Fatalf("commit: %d %s", code, b)
		}
		if got := fileKey(t, path); got != first || !keyWorks(c, first) {
			t.Errorf("the config file holds %q, want the committed key", got)
		}
		next := <-second
		if code, b := commitKey(c, first, next); code != http.StatusNoContent || fileKey(t, path) != next {
			t.Errorf("commit of the second stage's key: %d %s", code, b)
		}
	})
}

type readSignal struct {
	r    io.Reader
	done chan struct{}
	once sync.Once
}

func (s *readSignal) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err == io.EOF {
		s.once.Do(func() { close(s.done) })
	}
	return n, err
}
