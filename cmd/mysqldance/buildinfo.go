package main

import "runtime/debug"

// The values version and commit have when no ldflags were given.
const (
	defaultVersion = "dev"
	defaultCommit  = "unknown"
)

// resolveBuildInfo returns the version and commit to print. A value set by
// ldflags is kept as is. A value still at its default is replaced by what the
// Go toolchain recorded: the module version (set by go install ...@vX.Y.Z)
// and the VCS revision (set by go build in a checkout, "-dirty" appended when
// the tree had changes). When the toolchain recorded nothing, the default stays.
func resolveBuildInfo(version, commit string, info *debug.BuildInfo) (string, string) {
	if info == nil {
		return version, commit
	}
	if version == defaultVersion {
		// "(devel)" is what a build from a checkout records; it says nothing.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
	}
	if commit == defaultCommit {
		var revision string
		var modified bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if revision != "" {
			commit = revision
			if modified {
				commit += "-dirty"
			}
		}
	}
	return version, commit
}

// buildVersion returns the version and commit for --version.
func buildVersion() (string, string) {
	// info is nil when the binary carries no build info.
	info, _ := debug.ReadBuildInfo()
	return resolveBuildInfo(version, commit, info)
}
