package npm

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderDetectsNestedPackagesWhenRootHasNoManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, "mobile", `{"name":"mobile"}`)
	writePackage(t, root, "tools/broken", `{`)
	writePackage(t, root, "node_modules/ignored", `{"name":"ignored"}`)
	writePackage(t, root, ".git/ignored", `{"name":"ignored"}`)
	writePackage(t, root, ".cache/tooling", `{"name":"tooling-cache"}`)

	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("Detect() returned %d instances, want 2", len(instances))
	}
	want := []struct {
		id          scope.InstanceID
		packageRoot scope.Path
		code        string
	}{
		{id: "package/mobile", packageRoot: "mobile"},
		{id: "package/tools/broken", packageRoot: "tools/broken", code: "npm/package-json-unavailable"},
	}
	for index, raw := range instances {
		got, ok := raw.(npmInstance)
		if !ok {
			t.Fatalf("instance[%d] has type %T", index, raw)
		}
		code := ""
		if got.unavailable != nil {
			code = got.unavailable.Code
		}
		if got.Descriptor().ID != want[index].id || got.packageRoot != want[index].packageRoot || code != want[index].code {
			t.Errorf("instance[%d] = %+v, want %+v", index, got, want[index])
		}
	}
}

func TestProviderDoesNotTreatUndeclaredNestedManifestAsWorkspace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"root"}`)
	writePackage(t, root, "fixtures/example", `{"name":"example"}`)

	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Descriptor().ID != rootInstanceID {
		t.Fatalf("Detect() = %+v, want only root package", instances)
	}
}

func TestDiscoverNestedPackagesLimitIsExplicitlyUnresolved(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	writePackage(t, rootPath, "a", `{"name":"a"}`)
	writePackage(t, rootPath, "b", `{"name":"b"}`)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	paths, unavailable, err := discoverNestedPackages(context.Background(), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []scope.Path{"a"}) {
		t.Errorf("paths = %q, want [a]", paths)
	}
	if unavailable == nil || unavailable.Code != "npm/package-discovery-limit" {
		t.Fatalf("unavailable = %+v, want discovery limit", unavailable)
	}
}

func TestDiscoverNestedPackagesCancellation(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := discoverNestedPackages(ctx, root, maxDiscoveryEntries); err == nil {
		t.Fatal("discoverNestedPackages() error = nil, want cancellation")
	}
}
