package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/private"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

// Connect and Disconnect: core writes a client tool's settings on its own machine, as the desktop
// app's add-on for the tool does on the app's (next/app-tools/claude-code and codex). Connect sets
// in the tool's settings file what the tool's setup text holds (ClientSnippetFor), with core's
// address as programs on this machine reach it and a client key made for the tool, and keeps the
// file as it was in a record in core's state folder; Disconnect puts it back (clearClaudeSettings,
// clearCodexConfig) and removes the key Connect made. Only the tool's own settings file is
// written, in the home folder of the user core runs as; a link there, or a folder or file another
// user owns, is refused. No key goes to the log.

const (
	toolRecordFormat = 0
	toolRecordsDir   = "tools"
)

// toolRecord is what core keeps of a connect: the key it made and each file as it was before.
// It holds keys (the client key, and whatever the file held before), so it is private.
type toolRecord struct {
	Format int       `json:"format"`
	Tool   string    `json:"tool"`
	At     time.Time `json:"at"`
	// KeyID is the client key the connect made for the tool; empty when the tool kept a key core
	// already held.
	KeyID string           `json:"key_id,omitempty"`
	Files []toolRecordFile `json:"files"`
}

type toolRecordFile struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Mode    fs.FileMode `json:"mode,omitempty"`
	// MadeFolder: the file's folder did not exist and the connect made it.
	MadeFolder bool `json:"made_folder,omitempty"`
	// Original is the file before the first connect; Applied is what the last connect wrote.
	Original []byte `json:"original,omitempty"`
	Applied  []byte `json:"applied"`
}

// connectable reports whether core writes tool's settings itself: every file of its setup text
// is one it can change.
func connectable(tool string) bool {
	s, ok := ClientSnippetFor(tool, "", "", "")
	if !ok || len(s.Files) == 0 {
		return false
	}
	for _, f := range s.Files {
		if _, ok := toolFormats[f.Path]; !ok {
			return false
		}
	}
	return true
}

func (m *setupManager) toolsDir() private.Dir {
	return private.Dir{Path: filepath.Join(filepath.Dir(m.statePath), toolRecordsDir)}
}

// record reads the record of tool's connect; nil when there is none.
func (m *setupManager) record(tool string) (*toolRecord, error) {
	b, err := os.ReadFile(filepath.Join(m.toolsDir().Path, tool+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r toolRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("reading what pluxy kept of connecting %s: %v", tool, err)
	}
	if r.Format != toolRecordFormat || r.Tool != tool {
		return nil, fmt.Errorf("what pluxy kept of connecting %s has format %d; this pluxy does not read it", tool, r.Format)
	}
	return &r, nil
}

func (m *setupManager) hasRecord(tool string) bool {
	_, err := os.Lstat(filepath.Join(m.toolsDir().Path, tool+".json"))
	return err == nil
}

// saveRecord writes r, or with nil removes tool's record.
func (m *setupManager) saveRecord(tool string, r *toolRecord) error {
	d := m.toolsDir()
	root, err := d.OpenRoot(r != nil)
	if errors.Is(err, fs.ErrNotExist) && r == nil {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	name := tool + ".json"
	if r == nil {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return private.SyncDir(root)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return private.WriteFile(root, d.Path, ".tmp-", name, append(b, '\n'))
}

// refreshClient reads tool's settings again for the kept scan, so the next GET /setup shows what
// a connect or disconnect left.
func (m *setupManager) refreshClient(tool, home, coreURL string) {
	spec, ok := clientSpecByID(tool)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.found.Clients, func(c management.SetupClient) bool { return c.ID == tool })
	if i < 0 {
		return
	}
	m.found.Clients = slices.Clone(m.found.Clients)
	m.found.Clients[i].Config, m.found.Clients[i].State = spec.inspect(home, coreURL)
	if err := m.saveLocked(); err != nil {
		log.Printf("error: setup: saving %s: %v", m.statePath, err)
	}
}

// markConnect sets what the setup reply says of Connect on each client.
func (m *setupManager) markConnect(clients []management.SetupClient) {
	for i := range clients {
		c := &clients[i]
		c.CanConnect = connectable(c.ID) && c.State != management.ClientStateRefused && c.State != management.ClientStateUnchecked
		c.ConnectedByPluxy = connectable(c.ID) && m.hasRecord(c.ID)
	}
}

// ---- the tool's settings file ---------------------------------------------------------------

// toolFile is a tool's settings file opened for a change: its folder as a root checked to be a
// folder (not a link) of the user core runs as, and the file as read.
type toolFile struct {
	display string // ~/.claude/settings.json
	dir     string
	name    string
	root    *os.Root
	made    bool        // the folder did not exist: opening made it
	info    fs.FileInfo // nil when there is no file
	data    []byte
}

// openToolFile opens display (a path from the home folder, ~/...) in home. With create a missing
// folder is made, readable only by its owner (0700); without it, a missing folder is no file.
func openToolFile(home, display string, create bool) (*toolFile, error) {
	if home == "" || !filepath.IsAbs(home) {
		return nil, errors.New("pluxy does not know the home folder of the user it runs as")
	}
	rel, ok := strings.CutPrefix(display, "~/")
	if !ok {
		return nil, fmt.Errorf("%s is not a path in the home folder", display)
	}
	dirRel, name := path.Split(rel)
	folder := "~/" + strings.TrimSuffix(dirRel, "/")
	t := &toolFile{display: display, dir: filepath.Join(home, filepath.FromSlash(dirRel)), name: name}
	fi, err := os.Lstat(t.dir)
	if errors.Is(err, fs.ErrNotExist) {
		if !create {
			return t, nil
		}
		mkErr := os.Mkdir(t.dir, 0o700)
		if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
			return nil, mkErr
		}
		t.made = mkErr == nil
		fi, err = os.Lstat(t.dir)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, refuseFile("%s is a link: pluxy changes only a folder that is itself in the home folder", folder)
	case !fi.IsDir():
		return nil, refuseFile("%s is not a folder", folder)
	}
	h, err := os.Open(t.dir)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	hst, err := h.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, hst) {
		return nil, fmt.Errorf("%s changed while pluxy opened it; try again", folder)
	}
	if owned, err := ownedByUser(h); err != nil || !owned {
		return nil, cmpErr(err, refuseFile("%s belongs to another user than the one pluxy runs as", folder))
	}
	root, err := os.OpenRoot(t.dir)
	if err != nil {
		return nil, err
	}
	if rst, err := root.Stat("."); err != nil || !os.SameFile(rst, hst) {
		root.Close()
		return nil, cmpErr(err, fmt.Errorf("%s changed while pluxy opened it; try again", folder))
	}
	t.root = root
	if err := t.read(); err != nil {
		root.Close()
		return nil, err
	}
	return t, nil
}

func cmpErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}

// read reads the file: none is no data; a link, anything but a file, a file another user owns or
// one larger than maxClientConfig is refused.
func (t *toolFile) read() error {
	t.info, t.data = nil, nil
	fi, err := t.root.Lstat(t.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return refuseFile("%s is a link: pluxy changes only a file that is itself in the folder", t.display)
	case !fi.Mode().IsRegular():
		return refuseFile("%s is not a file", t.display)
	}
	f, err := t.root.OpenFile(t.name, os.O_RDONLY|readNoWait, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, st) {
		return fmt.Errorf("%s changed while pluxy read it; try again", t.display)
	}
	if owned, err := ownedByUser(f); err != nil || !owned {
		return cmpErr(err, refuseFile("%s belongs to another user than the one pluxy runs as", t.display))
	}
	b, err := io.ReadAll(io.LimitReader(f, maxClientConfig+1))
	if err != nil {
		return err
	}
	if len(b) > maxClientConfig {
		return refuseFile("%s is larger than %d bytes", t.display, maxClientConfig)
	}
	t.info, t.data = st, b
	return nil
}

// write replaces the file with data, mode mode, through a private temporary file renamed over it;
// nil data removes it. It refuses when the file is no longer the one read, or holds other bytes.
func (t *toolFile) write(data []byte, mode fs.FileMode) error {
	if t.root == nil {
		return fmt.Errorf("the folder of %s is gone", t.display)
	}
	was, wasInfo := t.data, t.info
	if err := t.read(); err != nil {
		return err
	}
	if (wasInfo == nil) != (t.info == nil) || wasInfo != nil && (!os.SameFile(wasInfo, t.info) || !bytes.Equal(was, t.data)) {
		return fmt.Errorf("%s changed while pluxy was changing it; try again", t.display)
	}
	if data == nil {
		if t.info == nil {
			return nil
		}
		if err := t.root.Remove(t.name); err != nil {
			return err
		}
		t.info, t.data = nil, nil
		return private.SyncDir(t.root)
	}
	f, tmp, err := private.CreateTemp(t.root, t.dir, "."+t.name+".pluxy-")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			_ = t.root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := t.root.Rename(tmp, t.name); err != nil {
		return err
	}
	done = true
	if err := private.SyncDir(t.root); err != nil {
		return err
	}
	return t.read()
}

// narrow sets the mode of the file read, unchanged, to mode: the file holds a key, which only its
// owner may read. It refuses when the file is no longer the one read.
func (t *toolFile) narrow(mode fs.FileMode) error {
	if t.root == nil || t.info == nil {
		return fmt.Errorf("%s is gone", t.display)
	}
	f, err := t.root.OpenFile(t.name, os.O_RDONLY|readNoWait, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(t.info, st) {
		return fmt.Errorf("%s changed while pluxy was changing it; try again", t.display)
	}
	return f.Chmod(mode)
}

func (t *toolFile) close() {
	if t.root != nil {
		t.root.Close()
	}
}

// undoFolder removes the folder opening made, when it is still empty.
func (t *toolFile) undoFolder() {
	if t.made {
		t.close()
		t.root = nil
		_ = os.Remove(t.dir)
	}
}

// ---- connect and disconnect --------------------------------------------------------------------

func canonicalTool(tool string) (string, management.SetupClient, error) {
	id, ok := clientSnippetID(tool)
	if !ok || !connectable(id) {
		return "", management.SetupClient{}, &editError{http.StatusNotFound, "not_found",
			fmt.Sprintf("pluxy cannot connect %q itself (it can connect: %s)", tool, strings.Join(connectableTools(), ", "))}
	}
	spec, _ := clientSpecByID(id)
	return id, management.SetupClient{ID: id, Name: spec.name}, nil
}

func connectableTools() []string {
	var out []string
	for _, id := range ClientSnippetTools {
		if connectable(id) {
			out = append(out, id)
		}
	}
	return out
}

// fileError is err as the API says it: a file pluxy will not change is a 422 with why; anything
// else is as writeChangeError says it.
func fileError(err error) error {
	var no *errNotOurs
	if errors.As(err, &no) {
		return &editError{http.StatusUnprocessableEntity, "refused", no.why}
	}
	return err
}

// clientNow is the tool as core reads its settings now.
func (c *Core) clientNow(tool, home, coreURL string) management.SetupClient {
	spec, _ := clientSpecByID(tool)
	client := management.SetupClient{ID: tool, Name: spec.name}
	_, _, _, found := c.setup.snapshot()
	if i := slices.IndexFunc(found.Clients, func(x management.SetupClient) bool { return x.ID == tool }); i >= 0 {
		client = found.Clients[i]
	}
	client.Config, client.State = spec.inspect(home, coreURL)
	list := []management.SetupClient{client}
	c.setup.markConnect(list)
	return list[0]
}

// ConnectClient points tool, on core's machine, at core: see the comment at the top of the file.
// label names a key made for the tool (checked by the caller); empty is the tool's name. model is
// the model the tool starts with (checked by the caller); empty picks the best match among the
// models core serves, and when core serves none leaves the tool's model settings alone.
func (c *Core) ConnectClient(ctx context.Context, tool, label, model string) (management.ClientConnection, error) {
	id, _, err := canonicalTool(tool)
	if err != nil {
		return management.ClientConnection{}, err
	}
	c.setup.toolsMu.Lock()
	defer c.setup.toolsMu.Unlock()

	listen, tlsEnable := c.setupCoreTarget()
	baseURL, err := server.LocalURL(listen, tlsEnable)
	if err != nil {
		return management.ClientConnection{}, &editError{http.StatusInternalServerError, "internal", "core's own address is not known: " + err.Error()}
	}
	home := userHomeDir()
	rec, err := c.setup.record(id)
	if err != nil {
		return management.ClientConnection{}, fileError(err)
	}
	paths, _ := ClientSnippetFor(id, baseURL, "<client-key>", "")
	files := make([]*toolFile, 0, len(paths.Files))
	defer func() {
		for _, f := range files {
			f.close()
		}
	}()
	fail := func(err error) (management.ClientConnection, error) {
		for _, f := range files {
			f.undoFolder()
		}
		return management.ClientConnection{}, fileError(err)
	}
	for _, p := range paths.Files {
		f, err := openToolFile(home, p.Path, true)
		if err != nil {
			return fail(err)
		}
		files = append(files, f)
	}

	// The key: the one the tool's settings hold when core holds it too, else a new one for the tool.
	key, keyID, made := "", "", false
	if t := toolFormats[paths.Files[0].Path].token(files[0].data); t != "" {
		if i := slices.IndexFunc(c.Config().ClientKeys, func(k string) bool { return sameSecret(k, t) }); i >= 0 {
			key = c.Config().ClientKeys[i]
			if rec != nil && rec.KeyID == c.keyID(key) {
				keyID = rec.KeyID
			}
		}
	}
	if key == "" {
		key = "sk-" + newKey()
		label := cmp.Or(label, paths.Name)
		err := c.WriteConfig("management API: client key for "+paths.Name, c.edit(func(d *configdoc.Doc) error {
			if err := d.AddClientKey(key); err != nil {
				return err
			}
			return d.SetClientKeyLabel(c.keyID(key), label)
		}))
		if err != nil {
			return fail(err)
		}
		keyID, made = c.keyID(key), true
	}
	dropKey := func() {
		if made {
			c.removeClientKey(keyID, true)
		}
	}

	model, _ = c.clientModel(ctx, id, model)
	snip, _ := clientSnippet(id, baseURL, key, model)
	next := &toolRecord{Format: toolRecordFormat, Tool: id, At: time.Now().UTC(), KeyID: keyID}
	outs := make([][]byte, len(files))
	for i, f := range files {
		out, err := toolFormats[snip.Files[i].Path].apply(f.data, snip.Files[i].Content)
		if err != nil {
			dropKey()
			return fail(err)
		}
		outs[i] = out
		rf := toolRecordFile{Path: f.display, Existed: f.info != nil, Original: f.data, Applied: out, MadeFolder: f.made}
		if f.info != nil {
			rf.Mode = f.info.Mode().Perm()
		}
		// A connect again keeps the file as it was before the first one.
		if rec != nil {
			if j := slices.IndexFunc(rec.Files, func(o toolRecordFile) bool { return o.Path == f.display }); j >= 0 {
				o := rec.Files[j]
				rf.Existed, rf.Original, rf.Mode, rf.MadeFolder = o.Existed, o.Original, o.Mode, o.MadeFolder
			}
		}
		next.Files = append(next.Files, rf)
	}
	// The record first: a write that ends half way still has what to put back.
	if err := c.setup.saveRecord(id, next); err != nil {
		dropKey()
		return fail(err)
	}
	reply := management.ClientConnection{KeyMade: made, Model: model}
	for i, f := range files {
		change := management.ClientFileChanged
		switch {
		case f.info == nil:
			change = management.ClientFileCreated
		case bytes.Equal(f.data, outs[i]):
			change = management.ClientFileUnchanged
		}
		mode := fs.FileMode(0o600)
		if f.info != nil {
			mode = f.info.Mode().Perm() &^ 0o077 // the file holds a key now: its owner's alone
		}
		var err error
		switch {
		case change != management.ClientFileUnchanged:
			err = f.write(outs[i], mode)
		case f.info.Mode().Perm()&0o077 != 0:
			// The bytes are already what connect writes, but others may read the key in them.
			err = f.narrow(mode)
		}
		if err != nil {
			// Put back the files already written, then what was kept before.
			for j := range i {
				back := next.Files[j]
				if !back.Existed {
					_ = files[j].write(nil, 0)
				} else {
					_ = files[j].write(back.Original, back.Mode)
				}
			}
			_ = c.setup.saveRecord(id, rec)
			dropKey()
			return fail(err)
		}
		reply.Files = append(reply.Files, management.ClientConnectionFile{Path: f.display, Change: change})
	}
	// A key an earlier connect made that the tool no longer sends goes.
	if rec != nil && rec.KeyID != "" && rec.KeyID != keyID && !c.keyInRecords(rec.KeyID) {
		c.removeClientKey(rec.KeyID, false)
	}
	log.Printf("setup: connected %s: %s; pluxy keeps the file as it was, for disconnect", paths.Name, joinPaths(reply.Files))
	coreURL := coreAddress(listen, tlsEnable)
	c.setup.refreshClient(id, home, coreURL)
	v := c.keyView(key)
	reply.Key = &v
	reply.Client = c.clientNow(id, home, coreURL)
	return reply, nil
}

// DisconnectClient puts back what ConnectClient changed in tool's settings, and removes the key
// it made for the tool when no other connected tool sends it and it is not the last client key.
func (c *Core) DisconnectClient(tool string) (management.ClientConnection, error) {
	id, client, err := canonicalTool(tool)
	if err != nil {
		return management.ClientConnection{}, err
	}
	c.setup.toolsMu.Lock()
	defer c.setup.toolsMu.Unlock()

	rec, err := c.setup.record(id)
	if err != nil {
		return management.ClientConnection{}, fileError(err)
	}
	if rec == nil {
		cfg, _ := ClientSnippetFor(id, "", "", "")
		return management.ClientConnection{}, &editError{http.StatusConflict, "conflict",
			fmt.Sprintf("pluxy did not connect %s, so it keeps nothing to put back: change %s by hand", client.Name, cfg.Files[0].Path)}
	}
	home := userHomeDir()
	files := make([]*toolFile, 0, len(rec.Files))
	defer func() {
		for _, f := range files {
			f.close()
		}
	}()
	type step struct {
		out    []byte
		mode   fs.FileMode
		change string
	}
	steps := make([]step, len(rec.Files))
	for i, rf := range rec.Files {
		format, ok := toolFormats[rf.Path]
		if !ok {
			return management.ClientConnection{}, fileError(fmt.Errorf("pluxy does not know how to put back %s", rf.Path))
		}
		f, err := openToolFile(home, rf.Path, false)
		if err != nil {
			return management.ClientConnection{}, fileError(err)
		}
		files = append(files, f)
		var original []byte
		if rf.Existed {
			original = rf.Original
			if original == nil {
				original = []byte{}
			}
		}
		if f.info == nil {
			// The file is gone: nothing of pluxy's is left in it.
			steps[i] = step{change: management.ClientFileUnchanged}
			continue
		}
		out, err := format.clear(original, rf.Applied, f.data)
		if err != nil {
			return management.ClientConnection{}, fileError(err)
		}
		st := step{out: out, mode: f.info.Mode().Perm(), change: management.ClientFileMerged}
		switch {
		case out == nil:
			st.change = management.ClientFileRemoved
		case bytes.Equal(out, f.data):
			st.change = management.ClientFileUnchanged
		case rf.Existed && bytes.Equal(out, original):
			st.change, st.mode = management.ClientFileRestored, rf.Mode
		}
		steps[i] = st
	}
	reply := management.ClientConnection{}
	for i, f := range files {
		if st := steps[i]; st.change != management.ClientFileUnchanged {
			if err := f.write(st.out, st.mode); err != nil {
				return management.ClientConnection{}, fileError(err)
			}
		}
		reply.Files = append(reply.Files, management.ClientConnectionFile{Path: rec.Files[i].Path, Change: steps[i].change})
	}
	if err := c.setup.saveRecord(id, nil); err != nil {
		return management.ClientConnection{}, fileError(err)
	}
	// A folder the connect made goes too, when nothing else is in it.
	for i, f := range files {
		if rec.Files[i].MadeFolder && f.root != nil {
			f.close()
			f.root = nil
			_ = os.Remove(f.dir)
		}
	}
	if rec.KeyID != "" && !c.keyInRecords(rec.KeyID) {
		reply.KeyRemoved = c.removeClientKey(rec.KeyID, false)
	}
	log.Printf("setup: disconnected %s: %s", client.Name, joinPaths(reply.Files))
	listen, tlsEnable := c.setupCoreTarget()
	coreURL := coreAddress(listen, tlsEnable)
	c.setup.refreshClient(id, home, coreURL)
	reply.Client = c.clientNow(id, home, coreURL)
	return reply, nil
}

// keyInRecords reports whether another connected tool's record names key id.
func (c *Core) keyInRecords(id string) bool {
	for _, tool := range connectableTools() {
		if r, err := c.setup.record(tool); err == nil && r != nil && r.KeyID == id {
			return true
		}
	}
	return false
}

// removeClientKey removes client key id and its label; it keeps the last key unless last is set
// (taking back a key a failed connect made, which leaves the keys as they were). It reports
// whether it removed the key.
func (c *Core) removeClientKey(id string, last bool) bool {
	errLast := errors.New("the last client key")
	removed := false
	err := c.WriteConfig("management API: client key removed", c.edit(func(d *configdoc.Doc) error {
		found := false
		left, err := d.RemoveClientKeys(func(k string) bool {
			drop := c.keyID(k) == id
			found = found || drop
			return drop
		})
		switch {
		case err != nil:
			return err
		case !found:
			return errNoFile
		case left == 0 && !last:
			return errLast
		}
		d.RemoveClientKeyLabel(id)
		removed = true
		return nil
	}))
	if err != nil && !errors.Is(err, errNoFile) && !errors.Is(err, errLast) {
		log.Printf("error: setup: removing the client key pluxy made for a tool: %v", err)
	}
	return err == nil && removed
}

func joinPaths(files []management.ClientConnectionFile) string {
	var parts []string
	for _, f := range files {
		parts = append(parts, f.Path+" "+f.Change)
	}
	return strings.Join(parts, ", ")
}

func (c *Core) handleConnect(m *server.Management) {
	reply := func(w http.ResponseWriter, v management.ClientConnection, err error) {
		if err != nil {
			c.writeChangeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
	m.Handle("POST /setup/clients/{id}/connect", func(w http.ResponseWriter, r *http.Request) {
		// label names a key made for the tool, as POST /client-keys's does; empty is the tool's name.
		// model names the model the tool starts with, as GET /setup/clients/{id}/paste's does;
		// empty picks the best match among those core serves (and writes no model when none is served).
		var req struct {
			Label string `json:"label"`
			Model string `json:"model"`
		}
		if !readOptionalJSON(w, r, &req) {
			return
		}
		label := ""
		if req.Label != "" {
			var ok bool
			if label, ok = c.checkKeyLabel(w, req.Label); !ok {
				return
			}
		}
		model, err := checkClientModel(req.Model)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		v, err := c.ConnectClient(r.Context(), r.PathValue("id"), label, model)
		reply(w, v, err)
	})
	m.Handle("POST /setup/clients/{id}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !readOptionalJSON(w, r, &req) {
			return
		}
		v, err := c.DisconnectClient(r.PathValue("id"))
		reply(w, v, err)
	})
}
