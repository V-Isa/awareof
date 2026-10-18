package app

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		info    *debug.BuildInfo
		ok      bool
		version string
	}{
		{name: "unavailable", version: developmentVersion},
		{name: "nil", ok: true, version: developmentVersion},
		{name: "empty", info: &debug.BuildInfo{}, ok: true, version: developmentVersion},
		{name: "development", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ok: true, version: developmentVersion},
		{name: "tagged module", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, ok: true, version: "v0.1.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := versionFromBuildInfo(test.info, test.ok); got != test.version {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, test.version)
			}
		})
	}
}

func TestCurrentVersionOverride(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = oldVersion })

	if got := currentVersion(); got != "v1.2.3" {
		t.Fatalf("currentVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestCurrentVersionUsesBuildInfoByDefault(t *testing.T) {
	oldVersion := Version
	Version = ""
	t.Cleanup(func() { Version = oldVersion })

	if got := currentVersion(); got == "" {
		t.Fatal("currentVersion() is empty")
	}
}
