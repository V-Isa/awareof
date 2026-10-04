package typescript

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestFindProjectCompiler(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "packages/app/tsconfig.json", `{}`, 0o600)
	packageRoot := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, packageRoot, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))

	installation, err := findProjectCompiler(root, scope.Path("packages/app/tsconfig.json"), platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if installation.major != 6 || installation.version != "6.0.3" || installation.coreName != "lib/_tsc.js" {
		t.Fatalf("installation = %+v, want TypeScript 6.0.3", installation)
	}
}

func TestFindProjectCompilerUsesNearestInstallation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "packages/app/tsconfig.json", `{}`, 0o600)
	writeCompilerPackage(t, filepath.Join(root, "node_modules", "typescript"), "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("root"))
	nearestRoot := filepath.Join(root, "packages", "app", "node_modules", "typescript")
	writeCompilerPackage(t, nearestRoot, "typescript", "6.0.4", filepath.Join("lib", "_tsc.js"), []byte("nearest"))

	installation, err := findProjectCompiler(root, "packages/app/tsconfig.json", platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if installation.version != "6.0.4" || installation.packageRoot != resolvedPath(t, nearestRoot) {
		t.Fatalf("installation = %+v, want nearest TypeScript 6.0.4", installation)
	}
}

func TestFindProjectCompilerSupportsSymlinkedPackage(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	storeRoot := filepath.Join(root, ".store", "typescript")
	writeCompilerPackage(t, storeRoot, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", ".store", "typescript"), filepath.Join(root, "node_modules", "typescript")); err != nil {
		t.Fatal(err)
	}

	installation, err := findProjectCompiler(root, "tsconfig.json", platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if installation.packageRoot != resolvedPath(t, storeRoot) {
		t.Fatalf("package root = %q, want %q", installation.packageRoot, resolvedPath(t, storeRoot))
	}
}

func TestFindProjectCompilerFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{name: "missing", want: "no installed TypeScript package"},
		{name: "wrong package name", prepare: func(t *testing.T, root string) {
			writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"not-typescript","version":"6.0.3"}`, 0o600)
		}, want: "installed compiler package is named"},
		{name: "unsupported major", prepare: func(t *testing.T, root string) {
			writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"typescript","version":"5.9.3"}`, 0o600)
		}, want: "unsupported"},
		{name: "malformed package", prepare: func(t *testing.T, root string) {
			writeTestFile(t, root, "node_modules/typescript/package.json", `{`, 0o600)
		}, want: "parse"},
		{name: "multiple package values", prepare: func(t *testing.T, root string) {
			writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"typescript","version":"6.0.3"}{}`, 0o600)
		}, want: "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
			if test.prepare != nil {
				test.prepare(t, root)
			}
			_, err := findProjectCompiler(root, "tsconfig.json", platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("findProjectCompiler() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestFindProjectCompilerTypeScript7PlatformPackage(t *testing.T) {
	t.Parallel()
	current := platform{goos: runtime.GOOS, goarch: runtime.GOARCH}
	platformName, err := platformPackageName(current)
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"typescript","version":"7.0.2"}`, 0o600)

	if _, err := findProjectCompiler(root, "tsconfig.json", current); err == nil || !strings.Contains(err.Error(), "platform package") {
		t.Fatalf("missing platform package error = %v", err)
	}
	platformRoot := filepath.Join(root, "node_modules", "@typescript", platformName)
	writeCompilerPackage(t, platformRoot, "@typescript/"+platformName, "7.0.1", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
	if _, err := findProjectCompiler(root, "tsconfig.json", current); err == nil || !strings.Contains(err.Error(), "7.0.1") || !strings.Contains(err.Error(), "7.0.2") {
		t.Fatalf("mismatched platform package error = %v, want both versions", err)
	}
	writeCompilerPackage(t, platformRoot, "@typescript/"+platformName, "7.0.2", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
	installation, err := findProjectCompiler(root, "tsconfig.json", current)
	if err != nil || installation.major != 7 || installation.name != "@typescript/"+platformName {
		t.Fatalf("installation = %+v, error = %v", installation, err)
	}
}

func TestInstallationFromTarget(t *testing.T) {
	t.Parallel()
	t.Run("TypeScript 6", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "typescript")
		writeCompilerPackage(t, root, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
		installation, err := installationFromTarget(safeexec.Target{Path: resolvedPath(t, filepath.Join(root, "lib", "_tsc.js"))}, platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
		if err != nil || installation.major != 6 {
			t.Fatalf("installation = %+v, error = %v", installation, err)
		}
	})
	t.Run("TypeScript 7", func(t *testing.T) {
		platformName, err := platformPackageName(platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
		if err != nil {
			t.Skip(err)
		}
		root := filepath.Join(t.TempDir(), platformName)
		writeCompilerPackage(t, root, "@typescript/"+platformName, "7.0.2", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
		installation, err := installationFromTarget(safeexec.Target{Path: resolvedPath(t, filepath.Join(root, "lib", executableName(runtime.GOOS)))}, platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
		if err != nil || installation.major != 7 {
			t.Fatalf("installation = %+v, error = %v", installation, err)
		}
	})
}

func TestInstallationFromTargetRejectsUnsupportedTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		packageN string
		version  string
		core     string
		content  []byte
		want     string
	}{
		{name: "outside lib", packageN: "typescript", version: "6.0.3", core: "_tsc.js", content: []byte("compiler"), want: "under lib"},
		{name: "TypeScript 6 wrapper", packageN: "typescript", version: "6.0.3", core: "lib/tsc.js", content: []byte("wrapper"), want: "lib/_tsc.js"},
		{name: "wrong TypeScript 6 package", packageN: "typescript-fork", version: "6.0.3", core: "lib/_tsc.js", content: []byte("compiler"), want: "lib/_tsc.js"},
		{name: "unsupported major", packageN: "typescript", version: "5.9.3", core: "lib/_tsc.js", content: []byte("compiler"), want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "package")
			writeCompilerPackage(t, root, test.packageN, test.version, filepath.FromSlash(test.core), test.content)
			_, err := installationFromTarget(safeexec.Target{Path: resolvedPath(t, filepath.Join(root, filepath.FromSlash(test.core)))}, platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("installationFromTarget() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestPackageFingerprintIdentity(t *testing.T) {
	t.Parallel()
	firstRoot := filepath.Join(t.TempDir(), "typescript")
	secondRoot := filepath.Join(t.TempDir(), "typescript")
	for _, root := range []string{firstRoot, secondRoot} {
		writeCompilerPackage(t, root, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
		writeTestFile(t, root, "lib/lib.dom.d.ts", "dom", 0o600)
	}
	first, err := loadInstallation(firstRoot, filepath.Join("lib", "_tsc.js"), packageMetadata{Name: "typescript", Version: "6.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadInstallation(secondRoot, filepath.Join("lib", "_tsc.js"), packageMetadata{Name: "typescript", Version: "6.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := packageFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := packageFingerprint(second)
	if err != nil || got != want {
		t.Fatalf("matching fingerprint = %q, %v; want %q", got, err, want)
	}

	writeTestFile(t, secondRoot, "README.md", "not evaluator input", 0o600)
	got, err = packageFingerprint(second)
	if err != nil || got != want {
		t.Fatalf("unrelated file fingerprint = %q, %v; want %q", got, err, want)
	}
	writeTestFile(t, secondRoot, "lib/lib.dom.d.ts", "patched", 0o600)
	got, err = packageFingerprint(second)
	if err != nil || got == want {
		t.Fatalf("patched standard library fingerprint = %q, %v; want a changed digest", got, err)
	}
}

func TestPackageFingerprintRejectsUnsafeTrees(t *testing.T) {
	t.Parallel()
	t.Run("no standard library", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "typescript")
		writeTestFile(t, root, "package.json", `{"name":"typescript","version":"6.0.3"}`, 0o600)
		writeTestFile(t, root, "lib/_tsc.js", "compiler", 0o600)
		installation, err := loadInstallation(root, filepath.Join("lib", "_tsc.js"), packageMetadata{Name: "typescript", Version: "6.0.3"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := packageFingerprint(installation); err == nil || !strings.Contains(err.Error(), "no standard-library") {
			t.Fatalf("packageFingerprint() error = %v, want missing library", err)
		}
	})
	t.Run("oversized compiler", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "typescript")
		writeCompilerPackage(t, root, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
		if err := os.Truncate(filepath.Join(root, "lib", "_tsc.js"), maxFingerprintFileSize+1); err != nil {
			t.Fatal(err)
		}
		installation, err := loadInstallation(root, filepath.Join("lib", "_tsc.js"), packageMetadata{Name: "typescript", Version: "6.0.3"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := packageFingerprint(installation); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("packageFingerprint() error = %v, want size limit", err)
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privileges vary on Windows")
		}
		root := filepath.Join(t.TempDir(), "typescript")
		writeCompilerPackage(t, root, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
		outside := filepath.Join(t.TempDir(), "lib.extra.d.ts")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "lib", "lib.extra.d.ts")); err != nil {
			t.Fatal(err)
		}
		installation, err := loadInstallation(root, filepath.Join("lib", "_tsc.js"), packageMetadata{Name: "typescript", Version: "6.0.3"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := packageFingerprint(installation); err == nil || !strings.Contains(err.Error(), "outside the package") {
			t.Fatalf("packageFingerprint() error = %v, want symlink escape", err)
		}
	})
}

func TestPlatformPackageName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		current platform
		want    string
		wantErr bool
	}{
		{current: platform{goos: "darwin", goarch: "arm64"}, want: "typescript-darwin-arm64"},
		{current: platform{goos: "darwin", goarch: "amd64"}, want: "typescript-darwin-x64"},
		{current: platform{goos: "linux", goarch: "amd64"}, want: "typescript-linux-x64"},
		{current: platform{goos: "linux", goarch: "arm"}, want: "typescript-linux-arm"},
		{current: platform{goos: "linux", goarch: "arm64"}, want: "typescript-linux-arm64"},
		{current: platform{goos: "linux", goarch: "loong64"}, want: "typescript-linux-loong64"},
		{current: platform{goos: "linux", goarch: "mips64le"}, want: "typescript-linux-mips64el"},
		{current: platform{goos: "linux", goarch: "ppc64"}, want: "typescript-linux-ppc64"},
		{current: platform{goos: "linux", goarch: "riscv64"}, want: "typescript-linux-riscv64"},
		{current: platform{goos: "linux", goarch: "s390x"}, want: "typescript-linux-s390x"},
		{current: platform{goos: "windows", goarch: "amd64"}, want: "typescript-win32-x64"},
		{current: platform{goos: "windows", goarch: "arm64"}, want: "typescript-win32-arm64"},
		{current: platform{goos: "freebsd", goarch: "amd64"}, wantErr: true},
		{current: platform{goos: "windows", goarch: "386"}, wantErr: true},
		{current: platform{goos: "plan9", goarch: "amd64"}, wantErr: true},
	}
	for _, test := range tests {
		got, err := platformPackageName(test.current)
		if (err != nil) != test.wantErr || got != test.want {
			t.Errorf("platformPackageName(%+v) = %q, %v; want %q, error=%v", test.current, got, err, test.want, test.wantErr)
		}
	}
}

func TestValidateNativeExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		goos    string
		header  []byte
		wantErr bool
	}{
		{name: "Linux ELF", goos: "linux", header: []byte{0x7f, 'E', 'L', 'F'}},
		{name: "macOS Mach-O", goos: "darwin", header: []byte{0xcf, 0xfa, 0xed, 0xfe}},
		{name: "Windows PE", goos: "windows", header: []byte{'M', 'Z', 0, 0}},
		{name: "wrong header", goos: "linux", header: []byte("nope"), wantErr: true},
		{name: "unsupported OS", goos: "freebsd", header: []byte{0x7f, 'E', 'L', 'F'}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "tsc")
			if err := os.WriteFile(name, test.header, 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateNativeExecutable(name, test.goos)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateNativeExecutable() error = %v, want error=%v", err, test.wantErr)
			}
		})
	}
}
