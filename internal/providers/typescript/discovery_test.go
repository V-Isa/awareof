package typescript

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderDetect(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "packages/app/tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "packages/app/tsconfig.build.json", `{}`, 0o600)
	writeTestFile(t, root, "node_modules/pkg/tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "build/tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "generated/tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "vendor/tool/tsconfig.json", `{}`, 0o600)
	current := New(&fakeRunner{}, fixedDiscoverer{})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("Detect() instances = %d, want 1", len(instances))
	}
	instance, ok := instances[0].(typescriptInstance)
	if !ok {
		t.Fatalf("Detect() instance type = %T, want typescriptInstance", instances[0])
	}
	want := []scope.Path{"build/tsconfig.json", "generated/tsconfig.json", "packages/app/tsconfig.json", "tsconfig.json", "vendor/tool/tsconfig.json"}
	if !reflect.DeepEqual(instance.configs, want) || instance.discovery != nil {
		t.Fatalf("Detect() = %+v, want configs %v", instance, want)
	}
}

func TestProviderDetectNoProjectAndLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	current := New(&fakeRunner{}, fixedDiscoverer{})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil || len(instances) != 0 {
		t.Fatalf("Detect() = %v, %v; want none", instances, err)
	}
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	current.scanLimit = 1
	instances, err = current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := instances[0].(typescriptInstance)
	if !ok {
		t.Fatalf("Detect() instance type = %T, want typescriptInstance", instances[0])
	}
	if instance.discovery == nil || instance.discovery.Code != "typescript/discovery-limit" {
		t.Fatalf("discovery = %+v, want limit", instance.discovery)
	}
}

func TestProviderDetectSymlinkConfigIsUnresolved(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "actual.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual.json", filepath.Join(root, "tsconfig.json")); err != nil {
		t.Fatal(err)
	}
	current := New(&fakeRunner{}, fixedDiscoverer{})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := instances[0].(typescriptInstance)
	if !ok {
		t.Fatalf("Detect() instance type = %T, want typescriptInstance", instances[0])
	}
	if instance.discovery == nil || instance.discovery.Code != "typescript/config-uninspectable" {
		t.Fatalf("discovery = %+v, want uninspectable", instance.discovery)
	}
}

func TestDiscoverConfigsContinuesAfterUnreadableDirectory(t *testing.T) {
	t.Parallel()
	filesystem := readDirFailureFS{
		FS: fstest.MapFS{
			"blocked/tsconfig.json": &fstest.MapFile{Data: []byte(`{}`)},
			"later/tsconfig.json":   &fstest.MapFile{Data: []byte(`{}`)},
		},
		fail: "blocked",
	}
	configs, issues, err := discoverConfigs(context.Background(), filesystem, maxScanEntries)
	if err != nil {
		t.Fatal(err)
	}
	if want := []scope.Path{"later/tsconfig.json"}; !reflect.DeepEqual(configs, want) {
		t.Fatalf("configs = %v, want %v", configs, want)
	}
	if len(issues) != 1 || issues[0].code != "typescript/discovery-unavailable" {
		t.Fatalf("issues = %+v, want one discovery-unavailable issue", issues)
	}
}

type readDirFailureFS struct {
	fs.FS
	fail string
}

func (f readDirFailureFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.fail {
		return file, err
	}
	return readDirFailureFile{File: file}, nil
}

type readDirFailureFile struct {
	fs.File
}

func (readDirFailureFile) ReadDir(int) ([]fs.DirEntry, error) {
	return nil, fs.ErrPermission
}

func TestSetupSelections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		want    []safeexec.Selection
	}{
		{name: "TypeScript 6 needs Node", version: "6.0.3", want: []safeexec.Selection{{Tool: "node"}, {Tool: "typescript"}}},
		{name: "TypeScript 7 is native", version: "7.0.2", want: []safeexec.Selection{{Tool: "typescript"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
			packageRoot := filepath.Join(root, "node_modules", "typescript")
			writeTestFile(t, packageRoot, "package.json", `{"name":"typescript","version":"`+test.version+`"}`, 0o600)
			if test.version[0] == '6' {
				writeTestFile(t, packageRoot, "lib/_tsc.js", "compiler", 0o600)
			} else {
				platformName, err := platformPackageName(platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
				if err != nil {
					t.Skip(err)
				}
				platformRoot := filepath.Join(root, "node_modules", "@typescript", platformName)
				writeCompilerPackage(t, platformRoot, "@typescript/"+platformName, test.version, filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
			}
			current := New(&fakeRunner{}, fixedDiscoverer{})
			instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			got, err := current.SetupSelections(root, instances)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("SetupSelections() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSetupSelectionsDiscoversTypeScript7NativeCore(t *testing.T) {
	t.Parallel()
	currentPlatform := platform{goos: runtime.GOOS, goarch: runtime.GOARCH}
	platformName, err := platformPackageName(currentPlatform)
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"typescript","version":"7.0.2"}`, 0o600)
	platformRoot := filepath.Join(root, "node_modules", "@typescript", platformName)
	writeCompilerPackage(t, platformRoot, "@typescript/"+platformName, "7.0.2", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
	manager, err := safeexec.NewManager(
		[]safeexec.Tool{{ID: typescriptTool, SelectionRequired: true}},
		safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	current := New(runner, manager)
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	selections, err := current.SetupSelections(root, instances)
	if err != nil {
		t.Fatal(err)
	}
	want := resolvedPath(t, filepath.Join(platformRoot, "lib", executableName(runtime.GOOS)))
	if len(selections) != 1 || selections[0] != (safeexec.Selection{Tool: typescriptTool, Path: want}) {
		t.Fatalf("SetupSelections() = %+v, want TypeScript 7 native core", selections)
	}
}

func TestSetupSelectionsDiscoversAndRemembersExternalTypeScript6(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectRoot := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectRoot, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalRoot := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalRoot, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	writeTestFile(t, externalRoot, "bin/tsc", "launcher", 0o700)
	manager, err := safeexec.NewManager(
		[]safeexec.Tool{{ID: nodeTool, Command: "node"}, {ID: typescriptTool, SelectionRequired: true}},
		safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	current := New(runner, manager)
	current.lookPath = func(string) (string, error) { return filepath.Join(externalRoot, "bin", "tsc"), nil }
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	selections, err := current.SetupSelections(root, instances)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := resolvedPath(t, filepath.Join(externalRoot, "lib", "_tsc.js"))
	want := []safeexec.Selection{{Tool: nodeTool}, {Tool: typescriptTool, Path: wantPath}}
	if !reflect.DeepEqual(selections, want) {
		t.Fatalf("SetupSelections() = %+v, want %+v", selections, want)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("setup executed the compiler: %+v", runner.requests)
	}
	statuses := manager.StatusesForSelections(root, []safeexec.Selection{want[1]})
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if _, err := manager.ApproveTarget(root, statuses[0].Target); err != nil {
		t.Fatal(err)
	}
	current.lookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	selections, err = current.SetupSelections(root, instances)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selections, want) {
		t.Fatalf("remembered SetupSelections() = %+v, want %+v", selections, want)
	}
}
