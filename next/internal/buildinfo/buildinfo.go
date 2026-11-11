// Package buildinfo says which build of pluxy runs: its version, the commit it was built from and
// its build date.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Version, Commit and Date are set at link time, for example:
//
//	go build -ldflags "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=1.2.3" ./cmd/pluxy
var (
	Version string
	Commit  string
	Date    string
)

// Get returns the build's version, commit and build date. Each is the value the link set; when
// the link set none, the value the Go toolchain recorded in the binary (the main module's
// version, the VCS revision and the VCS commit time); when that is unknown too, "dev" for the
// version and "unknown" for the commit and date. A main module version of "(devel)" is unknown.
func Get() (version, commit, date string) {
	version, commit, date = Version, Commit, Date
	if bi, ok := debug.ReadBuildInfo(); ok {
		if version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && commit == "":
				commit = s.Value
			case s.Key == "vcs.time" && date == "":
				date = s.Value
			}
		}
	}
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	if date == "" {
		date = "unknown"
	}
	return version, commit, date
}

// Line is the one line that says which build runs: pluxy <version> (<commit>, <date>).
func Line() string {
	version, commit, date := Get()
	return fmt.Sprintf("pluxy %s (%s, %s)", version, commit, date)
}
