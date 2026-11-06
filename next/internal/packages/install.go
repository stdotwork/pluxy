package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
)

// Platform is this core's platform, as registries and manifests name it.
const Platform = runtime.GOOS + "/" + runtime.GOARCH

// stagingDir is the folder under the packages folder where downloads are checked before they
// are moved into place.
const stagingDir = ".staging"

// Installer fetches and checks packages for one packages folder.
type Installer struct {
	// Dir is the packages folder.
	Dir string
	// Registries are searched in order when a request names none.
	Registries []Registry
	// Protocols are the protocol versions core speaks.
	Protocols []int
	// Client fetches https:// registries and archives. Default: DefaultClient.
	Client *http.Client
}

// Request says what to install.
type Request struct {
	ID string
	// Version: empty for the first release listed that runs here.
	Version string
	// Registry: a registry's name; empty searches every registry in order.
	Registry string
}

// Staged is a downloaded and checked package waiting in the staging folder.
type Staged struct {
	Installed
	in  *Installer
	dir string // the staging folder: dir/pkg holds the package
}

// Fetch finds the release req asks for, downloads its archive for this platform, checks its
// size and digest, unpacks it and checks the manifest inside. Nothing outside the staging
// folder changes; Commit moves the package into place.
func (in *Installer) Fetch(ctx context.Context, req Request) (*Staged, error) {
	if !host.ValidID(req.ID) {
		return nil, fmt.Errorf("%w: %q is not a package ID of the form publisher/name", ErrRefused, req.ID)
	}
	if req.Version != "" && !host.ValidVersion(req.Version) {
		return nil, fmt.Errorf("%w: %q is not a valid version", ErrRefused, req.Version)
	}
	client := in.Client
	if client == nil {
		client = DefaultClient()
	}
	reg, ix, err := in.find(ctx, client, req)
	if err != nil {
		return nil, err
	}
	rel, art, err := ix.pick(req.ID, req.Version, in.Protocols, Platform)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", reg.Name, err)
	}
	loc, err := resolve(reg.URL, art.URL)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", reg.Name, err)
	}

	if err := os.MkdirAll(filepath.Join(in.Dir, stagingDir), 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Join(in.Dir, stagingDir), "install-")
	if err != nil {
		return nil, err
	}
	s := &Staged{in: in, dir: dir, Installed: Installed{ID: req.ID, Version: rel.Version, Registry: reg.Name, URL: loc, SHA256: art.SHA256}}
	if err := s.fetch(ctx, client, art); err != nil {
		s.Discard()
		return nil, fmt.Errorf("%s %s from registry %s: %w", req.ID, rel.Version, reg.Name, err)
	}
	return s, nil
}

// find returns the registry to install req from and its index.
func (in *Installer) find(ctx context.Context, client *http.Client, req Request) (Registry, *Index, error) {
	if req.Registry != "" {
		i := slices.IndexFunc(in.Registries, func(r Registry) bool { return r.Name == req.Registry })
		if i < 0 {
			return Registry{}, nil, fmt.Errorf("%w: no registry named %q", ErrNotFound, req.Registry)
		}
		ix, err := ReadIndex(ctx, client, in.Registries[i])
		return in.Registries[i], ix, err
	}
	if len(in.Registries) == 0 {
		return Registry{}, nil, fmt.Errorf("%w: no registries are configured", ErrNotFound)
	}
	var errs []error
	for _, r := range in.Registries {
		ix, err := ReadIndex(ctx, client, r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, ok := ix.Packages[req.ID]; ok {
			return r, ix, nil
		}
	}
	if len(errs) > 0 {
		// A registry that could not be read may be the one listing it.
		return Registry{}, nil, fmt.Errorf("no readable registry lists %s: %w", req.ID, errors.Join(errs...))
	}
	return Registry{}, nil, fmt.Errorf("%w: no registry lists %s", ErrNotFound, req.ID)
}

func (s *Staged) fetch(ctx context.Context, client *http.Client, art Artifact) error {
	archive := filepath.Join(s.dir, "archive.tar.gz")
	if err := download(ctx, client, s.URL, art, archive); err != nil {
		return err
	}
	pkg := filepath.Join(s.dir, "pkg")
	if err := os.Mkdir(pkg, 0o755); err != nil {
		return err
	}
	if err := unpack(archive, pkg); err != nil {
		return err
	}
	if err := os.Remove(archive); err != nil {
		return err
	}
	return s.check(pkg)
}

// download copies the archive at loc to dst, refusing it unless it has exactly art.Size bytes
// with digest art.SHA256.
func download(ctx context.Context, client *http.Client, loc string, art Artifact, dst string) error {
	body, err := open(ctx, client, loc, art.Size)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, art.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w: reading the archive: %v", ErrFetch, err)
	}
	if n != art.Size {
		more := ""
		if n > art.Size {
			more = "more than "
		}
		return fmt.Errorf("%w: the archive is %s%d bytes; the registry lists %d", ErrRefused, more, n, art.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != art.SHA256 {
		return fmt.Errorf("%w: the archive's SHA-256 digest is %s, not the %s the registry pins", ErrRefused, got, art.SHA256)
	}
	return nil
}

// unpack unpacks the gzip-compressed tar archive at path into dir, which must be empty. Only
// regular files and folders inside dir are allowed, within the size and entry limits.
func unpack(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Every write goes through root, which refuses any path that leaves dir, lexically or
	// through a link: a second guard behind the name check below.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: the archive is not gzip-compressed: %v", ErrRefused, err)
	}
	tr := tar.NewReader(gz)
	var total int64
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: the archive is not a valid tar file: %v", ErrRefused, err)
		}
		if entries >= maxEntries {
			return fmt.Errorf("%w: the archive has more than %d entries", ErrRefused, maxEntries)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "" || name == "." {
			continue // the folder itself
		}
		local := filepath.FromSlash(name)
		if strings.Contains(name, `\`) || !filepath.IsLocal(local) {
			return fmt.Errorf("%w: the archive entry %q leaves the package folder", ErrRefused, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(local, 0o755); err != nil {
				return fmt.Errorf("%w: the archive entry %q: %v", ErrRefused, h.Name, err)
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > maxUnpacked {
				return fmt.Errorf("%w: the archive unpacks to more than %d bytes", ErrRefused, maxUnpacked)
			}
			if err := root.MkdirAll(filepath.Dir(local), 0o755); err != nil {
				return fmt.Errorf("%w: the archive entry %q: %v", ErrRefused, h.Name, err)
			}
			mode := fs.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			// O_EXCL: an entry never replaces another (a duplicate name, or one that differs only
			// in case on a case-insensitive file system).
			out, err := root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return fmt.Errorf("%w: the archive entry %q: %v", ErrRefused, h.Name, err)
			}
			n, err := io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil || n != h.Size {
				return fmt.Errorf("%w: the archive entry %q is cut short: %v", ErrRefused, h.Name, err)
			}
		default:
			return fmt.Errorf("%w: the archive entry %q is not a regular file or folder; archives hold only files and folders", ErrRefused, h.Name)
		}
	}
}

// check checks the unpacked package in dir against the release and this core, and records its
// fingerprint.
func (s *Staged) check(dir string) error {
	mpath := filepath.Join(dir, host.ManifestFile)
	if fi, err := os.Lstat(mpath); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: the archive has no %s at its top", ErrRefused, host.ManifestFile)
	}
	m, err := host.LoadManifest(mpath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if m.ID != s.ID || m.Version != s.Version {
		return fmt.Errorf("%w: the manifest is for %s %s, not the %s %s the registry lists", ErrRefused, m.ID, m.Version, s.ID, s.Version)
	}
	if !slices.ContainsFunc(m.Protocol, func(v int) bool { return slices.Contains(s.in.Protocols, v) }) {
		return fmt.Errorf("%w: the program speaks protocol %v; this core speaks %v", ErrRefused, m.Protocol, s.in.Protocols)
	}
	prog, err := m.Program()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if fi, err := os.Lstat(prog); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: the archive does not hold the program its manifest names for %s", ErrRefused, Platform)
	}
	if s.Fingerprint, err = host.Fingerprint(mpath); err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	return nil
}

// Commit moves the staged package into its folder, {Dir}/{publisher}/{name}/{version}, and
// returns its record (not yet saved: the caller saves the State). A folder already there for
// the version that inUse does not claim is left over from an interrupted install or removal and
// is replaced; one in use is a conflict.
func (s *Staged) Commit(inUse func(dir string) bool) (Installed, error) {
	defer s.Discard()
	final := s.Installed.Dir(s.in.Dir)
	if _, err := os.Lstat(final); err == nil {
		if inUse != nil && inUse(final) {
			return Installed{}, fmt.Errorf("%w: %s %s is still in use by a removed copy that is finishing its requests; try again once it has stopped", ErrConflict, s.ID, s.Version)
		}
		if err := os.RemoveAll(final); err != nil {
			return Installed{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Installed{}, err
	}
	if err := os.Rename(filepath.Join(s.dir, "pkg"), final); err != nil {
		return Installed{}, err
	}
	s.InstalledAt = time.Now().UTC()
	return s.Installed, nil
}

// Discard removes the staging folder.
func (s *Staged) Discard() {
	if s.dir != "" {
		os.RemoveAll(s.dir)
	}
}

// ClearStaging removes what interrupted installs left in the staging folder. Call it before
// any install runs.
func ClearStaging(dir string) error {
	return os.RemoveAll(filepath.Join(dir, stagingDir))
}

// Remove removes the folder of id at version, and its publisher's and name's folders when
// they are left empty.
func Remove(dir, id, version string) error {
	in := Installed{ID: id, Version: version}
	final := in.Dir(dir)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	name := filepath.Dir(final)
	_ = os.Remove(name)               // fails when another version is there
	_ = os.Remove(filepath.Dir(name)) // fails when another package of the publisher is there
	return nil
}
