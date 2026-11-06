package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one archive entry; typ 0 is a regular file.
type entry struct {
	name string
	typ  byte
	body string
	link string
}

func manifest(id, version, program string) string {
	b, _ := json.Marshal(map[string]any{"manifest_version": 0, "id": id, "version": version, "protocol": []int{0},
		"programs": map[string]string{Platform: program}})
	return string(b)
}

// good is a valid package archive's entries for test/ext 1.0.0.
func good() []entry {
	return []entry{{name: "pluxy-extension.json", body: manifest("test/ext", "1.0.0", "bin/ext")}, {name: "bin/ext", body: "#!/bin/sh\n"}}
}

func archive(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// localRegistry writes a registry offering arc as test/ext 1.0.0 and returns its index path.
// edit may change the release before it is written.
func localRegistry(t *testing.T, arc []byte, edit func(*Release)) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ext.tar.gz"), arc, 0o644); err != nil {
		t.Fatal(err)
	}
	rel := Release{Version: "1.0.0", Protocol: []int{0},
		Artifacts: map[string]Artifact{Platform: {URL: "ext.tar.gz", SHA256: digest(arc), Size: int64(len(arc))}}}
	if edit != nil {
		edit(&rel)
	}
	b, _ := json.Marshal(Index{Packages: map[string]Package{"test/ext": {Releases: []Release{rel}}}})
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func installer(t *testing.T, index string) (*Installer, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "packages")
	return &Installer{Dir: root, Registries: []Registry{{Name: "test", URL: index}}, Protocols: []int{0}}, root
}

// The archive and manifest checks: each refused archive leaves nothing installed and writes
// nothing outside the staging folder.
func TestFetchRefusesBadPackages(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		edit    func(*Release)
		want    string
	}{
		{"entry leaving the folder", append(good(), entry{name: "../escape", body: "x"}), nil, "leaves the package folder"},
		{"absolute entry", append(good(), entry{name: "/tmp/escape", body: "x"}), nil, "leaves the package folder"},
		{"symbolic link", append(good(), entry{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"}), nil, "not a regular file or folder"},
		{"hard link", append(good(), entry{name: "hard", typ: tar.TypeLink, link: "bin/ext"}), nil, "not a regular file or folder"},
		{"duplicate entry", append(good(), entry{name: "bin/ext", body: "again"}), nil, "bin/ext"},
		{"no manifest", []entry{{name: "bin/ext", body: "x"}}, nil, "no pluxy-extension.json"},
		{"other package's manifest", []entry{{name: "pluxy-extension.json", body: manifest("test/other", "1.0.0", "bin/ext")}, {name: "bin/ext", body: "x"}}, nil, "not the test/ext 1.0.0"},
		{"other version's manifest", []entry{{name: "pluxy-extension.json", body: manifest("test/ext", "2.0.0", "bin/ext")}, {name: "bin/ext", body: "x"}}, nil, "not the test/ext 1.0.0"},
		{"program missing", []entry{{name: "pluxy-extension.json", body: manifest("test/ext", "1.0.0", "bin/ext")}}, nil, "does not hold the program"},
		{"program is a folder", []entry{{name: "pluxy-extension.json", body: manifest("test/ext", "1.0.0", "bin")}, {name: "bin/", typ: tar.TypeDir}}, nil, "does not hold the program"},
		{"wrong digest", good(), func(r *Release) {
			a := r.Artifacts[Platform]
			a.SHA256 = strings.Repeat("0", 64)
			r.Artifacts[Platform] = a
		}, "digest"},
		{"shorter than listed", good(), func(r *Release) {
			a := r.Artifacts[Platform]
			a.Size++
			r.Artifacts[Platform] = a
		}, "bytes; the registry lists"},
		{"longer than listed", good(), func(r *Release) {
			a := r.Artifacts[Platform]
			a.Size--
			r.Artifacts[Platform] = a
		}, "more than"},
		{"url leaving the registry folder", good(), func(r *Release) {
			a := r.Artifacts[Platform]
			a.URL = "../ext.tar.gz"
			r.Artifacts[Platform] = a
		}, "inside the local registry's folder"},
		{"plain http url", good(), func(r *Release) {
			a := r.Artifacts[Platform]
			a.URL = "http://example.com/ext.tar.gz"
			r.Artifacts[Platform] = a
		}, "only https://"},
		{"protocol this core does not speak", good(), func(r *Release) { r.Protocol = []int{9} }, "speaks protocol"},
		{"no archive for this platform", good(), func(r *Release) { r.Artifacts = map[string]Artifact{"plan9/mips": r.Artifacts[Platform]} }, "no archive for"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outside := t.TempDir()
			t.Chdir(outside) // a relative escape would land here
			in, root := installer(t, localRegistry(t, archive(t, tt.entries), tt.edit))
			s, err := in.Fetch(context.Background(), Request{ID: "test/ext"})
			if err == nil {
				s.Discard()
				t.Fatal("installed")
			}
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v; want a refusal naming %q", err, tt.want)
			}
			left, _ := os.ReadDir(filepath.Join(root, stagingDir))
			if len(left) != 0 {
				t.Errorf("staging folder not cleaned: %v", left)
			}
			if _, err := os.Lstat(filepath.Join(root, "test")); !os.IsNotExist(err) {
				t.Errorf("something was installed: %v", err)
			}
			for _, p := range []string{filepath.Join(outside, "escape"), "/tmp/escape", filepath.Join(filepath.Dir(root), "escape")} {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s was written", p)
				}
			}
		})
	}
}

// A good package is unpacked with its program executable and moved into its own folder; its
// record holds the archive's digest and the unpacked files' fingerprint.
func TestFetchAndCommit(t *testing.T) {
	arc := archive(t, good())
	in, root := installer(t, localRegistry(t, arc, nil))
	s, err := in.Fetch(context.Background(), Request{ID: "test/ext"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Commit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "test/ext" || p.Version != "1.0.0" || p.Registry != "test" || p.SHA256 != digest(arc) || p.Fingerprint == "" {
		t.Errorf("record = %+v", p)
	}
	fi, err := os.Stat(filepath.Join(root, "test", "ext", "1.0.0", "bin", "ext"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("program: %v %v", fi, err)
	}
	if err := SaveState(root, State{p.ID: p}); err != nil {
		t.Fatal(err)
	}
	st, ok, err := LoadState(root)
	if err != nil || !ok || st["test/ext"].Fingerprint != p.Fingerprint {
		t.Errorf("record read back: %+v %v %v", st, ok, err)
	}
	if err := Remove(root, p.ID, p.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "test")); !os.IsNotExist(err) {
		t.Errorf("folders left after remove: %v", err)
	}
}

// An HTTPS registry: the archive's relative url resolves against the index's URL, and a
// redirect to plain http is not followed.
func TestHTTPSRegistry(t *testing.T) {
	arc := archive(t, good())
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reg/index.json", "/redirect/index.json":
			url := "ext.tar.gz"
			if strings.HasPrefix(r.URL.Path, "/redirect") {
				url = "moved.tar.gz"
			}
			_ = json.NewEncoder(w).Encode(Index{Packages: map[string]Package{"test/ext": {Releases: []Release{{Version: "1.0.0", Protocol: []int{0},
				Artifacts: map[string]Artifact{Platform: {URL: url, SHA256: digest(arc), Size: int64(len(arc))}}}}}}})
		case "/reg/ext.tar.gz":
			_, _ = w.Write(arc)
		case "/redirect/moved.tar.gz":
			http.Redirect(w, r, "http://"+r.Host+"/reg/ext.tar.gz", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = httpsRedirectsOnly

	in, _ := installer(t, srv.URL+"/reg/index.json")
	in.Client = client
	s, err := in.Fetch(context.Background(), Request{ID: "test/ext"})
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != srv.URL+"/reg/ext.tar.gz" {
		t.Errorf("archive URL = %s", s.URL)
	}
	s.Discard()

	in.Registries[0].URL = srv.URL + "/redirect/index.json"
	if s, err := in.Fetch(context.Background(), Request{ID: "test/ext"}); err == nil {
		s.Discard()
		t.Fatal("followed a redirect to http://")
	} else if !strings.Contains(err.Error(), "only https:// is allowed") {
		t.Errorf("error = %v", err)
	}
}

// A registry named by plain http:// or a relative path is refused before anything is read.
func TestRegistryLocations(t *testing.T) {
	for loc, ok := range map[string]bool{
		"https://example.com/index.json": true,
		"/srv/registry/index.json":       true,
		"http://example.com/index.json":  false,
		"ftp://example.com/index.json":   false,
		"registry/index.json":            false,
	} {
		if err := CheckLocation(loc); (err == nil) != ok {
			t.Errorf("CheckLocation(%q) = %v", loc, err)
		}
	}
}
