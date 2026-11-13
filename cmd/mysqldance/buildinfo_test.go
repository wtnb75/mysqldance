package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveBuildInfo(t *testing.T) {
	settings := func(kv ...string) []debug.BuildSetting {
		var s []debug.BuildSetting
		for i := 0; i < len(kv); i += 2 {
			s = append(s, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return s
	}
	tests := []struct {
		name                    string
		version, commit         string
		info                    *debug.BuildInfo
		wantVersion, wantCommit string
	}{
		{
			name:    "ldflags set: build info is ignored",
			version: "1.2.3", commit: "abc1234",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v9.9.9"},
				Settings: settings("vcs.revision", "ffff"),
			},
			wantVersion: "1.2.3", wantCommit: "abc1234",
		},
		{
			name:    "go install: module version and revision",
			version: "dev", commit: "unknown",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v1.4.0"},
				Settings: settings("vcs.revision", "0123456789abcdef"),
			},
			wantVersion: "v1.4.0", wantCommit: "0123456789abcdef",
		},
		{
			name:    "go build in a checkout: (devel) keeps dev, revision is used",
			version: "dev", commit: "unknown",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "0123456789abcdef"),
			},
			wantVersion: "dev", wantCommit: "0123456789abcdef",
		},
		{
			name:    "dirty tree is marked",
			version: "dev", commit: "unknown",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "0123456789abcdef", "vcs.modified", "true"),
			},
			wantVersion: "dev", wantCommit: "0123456789abcdef-dirty",
		},
		{
			name:    "no VCS information: defaults stay",
			version: "dev", commit: "unknown",
			info:        &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantVersion: "dev", wantCommit: "unknown",
		},
		{
			name:    "only version set by ldflags: commit still falls back",
			version: "1.2.3", commit: "unknown",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v9.9.9"},
				Settings: settings("vcs.revision", "ffff"),
			},
			wantVersion: "1.2.3", wantCommit: "ffff",
		},
		{
			name:    "only commit set by ldflags: version still falls back",
			version: "dev", commit: "abc1234",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v9.9.9"},
				Settings: settings("vcs.revision", "ffff", "vcs.modified", "true"),
			},
			wantVersion: "v9.9.9", wantCommit: "abc1234",
		},
		{
			name:    "modified without a revision does not touch the default",
			version: "dev", commit: "unknown",
			info:        &debug.BuildInfo{Settings: settings("vcs.modified", "true")},
			wantVersion: "dev", wantCommit: "unknown",
		},
		{
			name:    "no build info at all",
			version: "dev", commit: "unknown",
			info:        nil,
			wantVersion: "dev", wantCommit: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotV, gotC := resolveBuildInfo(tt.version, tt.commit, tt.info)
			if gotV != tt.wantVersion || gotC != tt.wantCommit {
				t.Errorf("resolveBuildInfo() = (%q, %q), want (%q, %q)", gotV, gotC, tt.wantVersion, tt.wantCommit)
			}
		})
	}
}
