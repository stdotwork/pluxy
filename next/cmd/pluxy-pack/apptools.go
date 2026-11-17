package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// appToolFixtures are the app-tools folders that exist only for the app's own tests: never
// packed.
var appToolFixtures = []string{"usage-fixture"}

// appManifest is the part of an app tool's manifest (manifest_version 1, next/app/app-host) the
// pack tool reads. An app tool's pages are under app.ui, where the app reads them.
type appManifest struct {
	ManifestVersion int    `json:"manifest_version"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	App             *struct {
		Tier     string            `json:"tier"`
		Programs map[string]string `json:"programs"`
		UI       *struct {
			Version int    `json:"version"`
			Dir     string `json:"dir"`
		} `json:"ui,omitempty"`
	} `json:"app"`
}

// loadAppTools reads every folder under root/app-tools with a native tool's manifest, in folder
// order, but the test fixtures.
func loadAppTools(root string) ([]extension, error) {
	dir := filepath.Join(root, "app-tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("app-tools folder: %w", err)
	}
	var tools []extension
	for _, entry := range entries {
		if !entry.IsDir() || slices.Contains(appToolFixtures, entry.Name()) {
			continue
		}
		toolDir := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(filepath.Join(toolDir, manifestFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var m appManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("app-tools/%s: %w", entry.Name(), err)
		}
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("app-tools/%s: %w", entry.Name(), err)
		}
		e := extension{name: entry.Name(), dir: toolDir, raw: raw, manifest: manifest{
			ID:       m.ID,
			Version:  m.Version,
			Programs: m.App.Programs,
			UI:       m.App.UI,
		}}
		tools = append(tools, e)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("no app tool with a %s under %s", manifestFile, dir)
	}
	return tools, nil
}

// validate checks what the pack tool relies on in an app tool's manifest: a native tool naming
// a program inside its package for each platform, a `.exe` on Windows only.
func (m *appManifest) validate() error {
	if m.ManifestVersion != 1 {
		return fmt.Errorf("manifest_version %d is not an app tool's (1)", m.ManifestVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q is not of the form publisher/name", m.ID)
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q is not 1 to 64 letters, digits, '.', '+', '_' and '-' starting with a letter or digit", m.Version)
	}
	if m.App == nil || m.App.Tier != "native" || len(m.App.Programs) == 0 {
		return errors.New("not a native tool with programs (app.tier native, app.programs)")
	}
	for platform, program := range m.App.Programs {
		goos, goarch, ok := strings.Cut(platform, "/")
		if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
			return fmt.Errorf("app.programs: %q is not of the form GOOS/GOARCH", platform)
		}
		if !storePath(program) {
			return fmt.Errorf("program for %s must be a path inside the package folder", platform)
		}
		if exe := strings.HasSuffix(program, ".exe"); exe != (goos == "windows") {
			return fmt.Errorf("program for %s is %s: a .exe on Windows and only there", platform, program)
		}
	}
	if m.App.UI != nil {
		if !storePath(m.App.UI.Dir) {
			return errors.New("app.ui.dir must be a folder inside the package folder")
		}
	}
	return nil
}

// storePath reports whether p is a path the app keeps inside a package folder, as the app's
// store_path checks archive entries and the ui folder: slash-separated parts, none empty, `.` or
// `..`, and no backslash, NUL or `:` (a drive or a stream on Windows).
func storePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00:") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// packAppTools packs the app tools for each of platforms their manifests name, from the
// programs built in programs, into out/app-tools with the app's registry index. A platform a
// manifest does not name is left out for that tool; a program missing for one it names fails.
func packAppTools(root, out, programs string, platforms []string, only string, stdout io.Writer) error {
	tools, err := loadAppTools(root)
	if err != nil {
		return err
	}
	tools, err = selectExtensions(tools, only, "app tool")
	if err != nil {
		return err
	}
	dest := filepath.Join(out, "app-tools")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	ix := index{RegistryVersion: 0, Packages: map[string]indexPackage{}}
	count := 0
	// The programs folder holds one file per name and platform: two tools naming the same program
	// file name for one platform would both be packed with whichever was built last.
	builtBy := map[string]string{}
	for _, tool := range tools {
		if _, ok := ix.Packages[tool.manifest.ID]; ok {
			return fmt.Errorf("two app tools have the package ID %s", tool.manifest.ID)
		}
		release := indexRelease{Version: tool.manifest.Version, Artifacts: map[string]indexArtifact{}}
		for _, platform := range platforms {
			program, ok := tool.manifest.Programs[platform]
			if !ok {
				continue
			}
			goos, goarch, _ := strings.Cut(platform, "/")
			key := platform + " " + path.Base(program)
			if other, ok := builtBy[key]; ok {
				return fmt.Errorf("%s and %s both name the program file %s for %s", other, tool.manifest.ID, path.Base(program), platform)
			}
			builtBy[key] = tool.manifest.ID
			built := filepath.Join(programs, goos+"-"+goarch, path.Base(program))
			// Not following a link: the program packed is the file built there, not what a link
			// left in the programs folder points to.
			info, err := os.Lstat(built)
			if err != nil {
				return fmt.Errorf("%s: its program for %s: %w", tool.manifest.ID, platform, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: its program for %s, %s, is not a regular file (a link or other)", tool.manifest.ID, platform, built)
			}
			archive, err := tool.archive(program, built)
			if err != nil {
				return err
			}
			name := archiveName(tool.manifest, platform)
			if err := os.WriteFile(filepath.Join(dest, name), archive, 0o644); err != nil {
				return err
			}
			sum := sha256.Sum256(archive)
			release.Artifacts[platform] = indexArtifact{
				URL:    name,
				SHA256: hex.EncodeToString(sum[:]),
				Size:   int64(len(archive)),
			}
			count++
			fmt.Fprintf(stdout, "%s %s %s\n", tool.manifest.ID, tool.manifest.Version, platform)
		}
		if len(release.Artifacts) > 0 {
			ix.Packages[tool.manifest.ID] = indexPackage{Releases: []indexRelease{release}}
		}
	}
	if count == 0 {
		return errors.New("no app tool names any of the platforms asked for")
	}
	body, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	indexPath := filepath.Join(dest, "index.json")
	if err := os.WriteFile(indexPath, body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d archives and %s\n", count, indexPath)
	return nil
}
