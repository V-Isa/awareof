package jest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

type fakeResolver struct {
	target safeexec.Target
	err    error
	calls  int
}

func (r *fakeResolver) Resolve(_ string, _ safeexec.ToolID) (safeexec.Target, error) {
	r.calls++
	return r.target, r.err
}

type fakeRunner struct {
	response safeexec.Response
	err      error
	requests []safeexec.Request
	run      func(context.Context, safeexec.Request) (safeexec.Response, error)
}

func (r *fakeRunner) Run(ctx context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	if r.run != nil {
		return r.run(ctx, request)
	}
	return r.response, r.err
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func evaluatorFixture(t *testing.T, contextRoot, version string) (string, *fakeResolver) {
	t.Helper()
	repository := t.TempDir()
	manifest := `{"private":true,"devDependencies":{"jest":"` + version + `"}}`
	writeFile(t, repository, filepath.ToSlash(filepath.Join(contextRoot, "package.json")), manifest)
	writeFile(t, repository, filepath.ToSlash(filepath.Join(contextRoot, "jest.config.js")), "module.exports = {};\n")
	writeFile(t, repository, "node_modules/jest/package.json", `{"name":"jest","version":"`+version+`"}`)
	writeFile(t, repository, "node_modules/jest/bin/jest.js", "require('jest-cli/bin/jest');\n")
	node := filepath.Join(t.TempDir(), "node")
	header := []byte{0x7f, 'E', 'L', 'F'}
	switch runtime.GOOS {
	case "darwin":
		header = []byte{0xcf, 0xfa, 0xed, 0xfe}
	case "windows":
		node += ".exe"
		header = []byte{'M', 'Z', 0, 0}
	}
	if err := os.WriteFile(node, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return repository, &fakeResolver{target: safeexec.Target{Tool: nodeTool, Path: node, Origin: safeexec.ExternalOrigin}}
}

func detectedInstance(t *testing.T, current *Provider, root string) provider.Instance {
	t.Helper()
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %#v, want one", instances)
	}
	return instances[0]
}

func requireJestInstance(t *testing.T, instances []provider.Instance) jestInstance {
	t.Helper()
	if len(instances) != 1 {
		t.Fatalf("instances = %#v, want one", instances)
	}
	current, ok := instances[0].(jestInstance)
	if !ok {
		t.Fatalf("instance type = %T", instances[0])
	}
	return current
}

func allowJest(t *testing.T) provider.EvaluationContext {
	t.Helper()
	result, err := provider.NewEvaluationContext([]scope.ProviderID{providerID})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProviderContract(t *testing.T) {
	t.Parallel()
	current := New(nil, nil)
	if current.ID() != providerID {
		t.Fatalf("ID() = %q", current.ID())
	}
	var _ provider.RepositoryCodeProvider = current
	if _, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, nil, nil); err == nil {
		t.Fatal("Evaluate() error = nil")
	}
	wrong := jestInstance{descriptor: provider.InstanceDescriptor{Provider: providerID, ID: "wrong"}}
	if _, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, wrong, []scope.Path{"x"}); err == nil {
		t.Fatal("Evaluate() wrong instance error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid := jestInstance{descriptor: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID}}
	if _, err := current.Evaluate(ctx, provider.EvaluationContext{}, provider.Repository{}, valid, []scope.Path{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate() error = %v", err)
	}
}
