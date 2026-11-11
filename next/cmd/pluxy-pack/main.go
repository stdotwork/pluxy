// Command pluxy-pack builds every extension in this module as a release archive for each
// platform it is asked for, and writes the bundled registry index that installs them.
//
// Run it from the next module (the folder with go.mod):
//
//	go run ./cmd/pluxy-pack -out dist -platforms linux/amd64,darwin/arm64
//
// The archives and the index go into <out>/bundled. The index is a registry index: uploaded
// beside the archives to an https:// location, it serves installs from there too.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// manifestFile is the package manifest's name at the top of a release archive.
const manifestFile = "pluxy-extension.json"

// usage is the text -h prints.
const usage = `pluxy-pack builds the release archives of the extensions in ./extensions and the registry
index that installs them. Run it from the next module (the folder with go.mod).

usage: pluxy-pack -out <folder> [-platforms <goos>/<goarch>,...] [-extensions <name>,...]

For every extension and platform it builds the program, writes
<folder>/bundled/<publisher>-<name>-<version>-<goos>-<goarch>.tar.gz and lists every archive
in <folder>/bundled/index.json. Start core with -bundled <folder>/bundled/index.json (or put
the bundled folder beside the pluxy program) to install and enable them on its first start.

flags:
`

// The package ID and version rules of the protocol README, "Package manifest".
var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pluxy-pack:", err)
		os.Exit(1)
	}
}

// run parses args, packs the extensions and writes the index.
func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pluxy-pack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "output folder (required)")
	platformsFlag := flags.String("platforms", runtime.GOOS+"/"+runtime.GOARCH, "comma-separated GOOS/GOARCH platforms (default: the host platform)")
	extensionsFlag := flags.String("extensions", "", "comma-separated extension folder names to pack (default: all)")
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *out == "" {
		flags.Usage()
		return errors.New("-out is required")
	}
	platforms, err := parsePlatforms(*platformsFlag)
	if err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	extensions, err := loadExtensions(root)
	if err != nil {
		return err
	}
	extensions, err = selectExtensions(extensions, *extensionsFlag)
	if err != nil {
		return err
	}

	bundled := filepath.Join(*out, "bundled")
	if err := os.MkdirAll(bundled, 0o755); err != nil {
		return err
	}
	buildDir, err := os.MkdirTemp("", "pluxy-pack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)

	ix := index{RegistryVersion: 0, Packages: map[string]indexPackage{}}
	count := 0
	for _, ext := range extensions {
		if _, ok := ix.Packages[ext.manifest.ID]; ok {
			return fmt.Errorf("two extensions have the package ID %s", ext.manifest.ID)
		}
		release := indexRelease{Version: ext.manifest.Version, Protocol: slices.Clone(ext.manifest.Protocol), Artifacts: map[string]indexArtifact{}}
		for _, platform := range platforms {
			program, ok := ext.manifest.Programs[platform]
			if !ok {
				return fmt.Errorf("%s has no program for %s in its manifest", ext.manifest.ID, platform)
			}
			goos, goarch, _ := strings.Cut(platform, "/")
			built := filepath.Join(buildDir, ext.name+"-"+goos+"-"+goarch)
			if err := buildProgram(root, "./extensions/"+ext.name, built, goos, goarch, stderr); err != nil {
				return err
			}
			archive, err := ext.archive(program, built)
			if err != nil {
				return err
			}
			name := archiveName(ext.manifest, platform)
			if err := os.WriteFile(filepath.Join(bundled, name), archive, 0o644); err != nil {
				return err
			}
			sum := sha256.Sum256(archive)
			release.Artifacts[platform] = indexArtifact{
				URL:    name,
				SHA256: hex.EncodeToString(sum[:]),
				Size:   int64(len(archive)),
			}
			count++
			fmt.Fprintf(stdout, "%s %s %s\n", ext.manifest.ID, ext.manifest.Version, platform)
		}
		ix.Packages[ext.manifest.ID] = indexPackage{Releases: []indexRelease{release}}
	}

	body, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	indexPath := filepath.Join(bundled, "index.json")
	if err := os.WriteFile(indexPath, body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d archives and %s\n", count, indexPath)
	return nil
}

// parsePlatforms parses the comma-separated GOOS/GOARCH list, dropping repeats.
func parsePlatforms(s string) ([]string, error) {
	var platforms []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		goos, goarch, ok := strings.Cut(p, "/")
		if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
			return nil, fmt.Errorf("platform %q is not of the form GOOS/GOARCH", p)
		}
		if !slices.Contains(platforms, p) {
			platforms = append(platforms, p)
		}
	}
	if len(platforms) == 0 {
		return nil, errors.New("-platforms names no platform")
	}
	return platforms, nil
}

// moduleRoot returns the folder of the next module, found from the folder this program runs
// in and its parents.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run inside the next module: no go.mod in this folder or its parents")
		}
		dir = parent
	}
}

// extension is one extension folder with a package manifest.
type extension struct {
	name     string // the folder name under extensions/
	dir      string // the folder's path
	manifest manifest
	raw      []byte // the manifest's bytes, as the archive holds them
}

// manifest is the part of a package manifest the pack tool reads.
type manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	ID              string            `json:"id"`
	Version         string            `json:"version"`
	Protocol        []int             `json:"protocol"`
	Programs        map[string]string `json:"programs"`
	UI              *struct {
		Version int    `json:"version"`
		Dir     string `json:"dir"`
	} `json:"ui,omitempty"`
}

// loadExtensions reads every extension folder under root/extensions that has a manifest, in
// folder order.
func loadExtensions(root string) ([]extension, error) {
	dir := filepath.Join(root, "extensions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("extensions folder: %w", err)
	}
	var extensions []extension
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		extDir := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(filepath.Join(extDir, manifestFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		extensions = append(extensions, extension{name: entry.Name(), dir: extDir, manifest: m, raw: raw})
	}
	if len(extensions) == 0 {
		return nil, fmt.Errorf("no extension with a %s under %s", manifestFile, dir)
	}
	return extensions, nil
}

// validate checks what the pack tool relies on in a manifest.
func (m *manifest) validate() error {
	if m.ManifestVersion != 0 {
		return fmt.Errorf("manifest_version %d is not supported", m.ManifestVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q is not of the form publisher/name", m.ID)
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q is not 1 to 64 letters, digits, '.', '+', '_' and '-' starting with a letter or digit", m.Version)
	}
	if len(m.Protocol) == 0 {
		return errors.New("protocol must list at least one version")
	}
	for platform, program := range m.Programs {
		if program == "" || !filepath.IsLocal(filepath.FromSlash(program)) || strings.Contains(program, `\`) {
			return fmt.Errorf("program for %s must be a path inside the package folder", platform)
		}
	}
	if m.UI != nil && m.UI.Dir != "" {
		if !filepath.IsLocal(filepath.FromSlash(m.UI.Dir)) || strings.Contains(m.UI.Dir, `\`) {
			return errors.New("ui.dir must be a folder inside the package folder")
		}
	}
	return nil
}

// selectExtensions filters extensions by the comma-separated folder names or package IDs in
// list.
func selectExtensions(extensions []extension, list string) ([]extension, error) {
	var names []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return extensions, nil
	}
	var chosen []extension
	for _, ext := range extensions {
		if slices.Contains(names, ext.name) || slices.Contains(names, ext.manifest.ID) {
			chosen = append(chosen, ext)
		}
	}
	var unknown []string
	for _, name := range names {
		if !slices.ContainsFunc(extensions, func(ext extension) bool {
			return ext.name == name || ext.manifest.ID == name
		}) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		known := make([]string, 0, len(extensions))
		for _, ext := range extensions {
			known = append(known, ext.name+" ("+ext.manifest.ID+")")
		}
		return nil, fmt.Errorf("-extensions: no extension is named %s; the extensions are: %s", strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	return chosen, nil
}

// archiveName returns the archive's file name for platform.
func archiveName(m manifest, platform string) string {
	goos, goarch, _ := strings.Cut(platform, "/")
	name := strings.ReplaceAll(m.ID, "/", "-")
	return fmt.Sprintf("%s-%s-%s-%s.tar.gz", name, m.Version, goos, goarch)
}

// buildProgram builds the extension at pkg, relative to root, into out for goos/goarch.
func buildProgram(root, pkg, out, goos, goarch string, stderr io.Writer) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w", "-o", out, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s for %s/%s: %w", pkg, goos, goarch, err)
	}
	return nil
}

// archive packs the extension as a release archive, with the program built at programPath at
// the path its manifest names.
func (e extension) archive(programName, programPath string) ([]byte, error) {
	program, err := os.ReadFile(programPath)
	if err != nil {
		return nil, err
	}
	entries := []archiveEntry{{name: programName, mode: 0o755, body: program}}
	ui, err := e.uiFiles()
	if err != nil {
		return nil, err
	}
	entries = append(entries, ui...)
	slices.SortFunc(entries, func(a, b archiveEntry) int { return strings.Compare(a.name, b.name) })
	entries = append([]archiveEntry{{name: manifestFile, mode: 0o644, body: e.raw}}, entries...)

	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.name] {
			return nil, fmt.Errorf("%s: the archive would hold %s twice", e.manifest.ID, entry.name)
		}
		seen[entry.name] = true
		if !filepath.IsLocal(filepath.FromSlash(entry.name)) || strings.Contains(entry.name, `\`) {
			return nil, fmt.Errorf("%s: the archive entry %q leaves the package folder", e.manifest.ID, entry.name)
		}
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.body)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(entry.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// archiveEntry is one file in a release archive.
type archiveEntry struct {
	name string
	mode int64
	body []byte
}

// uiFiles returns the files of the extension's ui folder, by their path inside the package.
func (e extension) uiFiles() ([]archiveEntry, error) {
	if e.manifest.UI == nil || e.manifest.UI.Dir == "" {
		return nil, nil
	}
	root := filepath.Join(e.dir, filepath.FromSlash(e.manifest.UI.Dir))
	var entries []archiveEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: the ui folder may hold only regular files and folders; %s is not one", e.manifest.ID, p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		entries = append(entries, archiveEntry{name: path.Join(e.manifest.UI.Dir, filepath.ToSlash(rel)), mode: mode, body: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// index is a registry index, version 0.
type index struct {
	RegistryVersion int                     `json:"registry_version"`
	Packages        map[string]indexPackage `json:"packages"`
}

type indexPackage struct {
	Releases []indexRelease `json:"releases"`
}

type indexRelease struct {
	Version   string                   `json:"version"`
	Protocol  []int                    `json:"protocol"`
	Artifacts map[string]indexArtifact `json:"artifacts"`
}

type indexArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
