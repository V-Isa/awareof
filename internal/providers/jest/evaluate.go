package jest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

var nodeUnsetEnvironment = []string{
	"FORCE_COLOR",
	"NODE_COMPILE_CACHE",
	"NODE_COMPILE_CACHE_PORTABLE",
	"NODE_COMPILE_CACHE_READONLY",
	"NODE_INSPECTOR_IPC",
	"NODE_OPTIONS",
	"NODE_PATH",
	"NODE_PRESERVE_SYMLINKS",
	"NODE_PRESERVE_SYMLINKS_MAIN",
	"NODE_REDIRECT_WARNINGS",
	"NODE_REPORT_DIRECTORY",
	"NODE_REPORT_FILENAME",
	"NODE_REPORT_ON_FATALERROR",
	"NODE_REPORT_ON_SIGNAL",
	"NODE_REPORT_SIGNAL",
	"NODE_REPORT_UNCAUGHT_EXCEPTION",
	"NODE_V8_COVERAGE",
	"VSCODE_INSPECTOR_OPTIONS",
}

func (p *Provider) evaluateContext(
	ctx context.Context,
	repositoryRoot string,
	current jestContext,
	installation jestInstallation,
	config selectedConfig,
	paths []scope.Path,
) (_ map[scope.Path]membershipFact, _ *scope.Explanation, _ safeexec.ToolID, returnErr error) {
	facts := make(map[scope.Path]membershipFact, len(paths))
	if p.runner == nil {
		return nil, nil, "", errors.New("jest command runner is nil")
	}
	if p.resolver == nil {
		return nil, nil, "", errors.New("jest tool resolver is nil")
	}

	node, problem, err := p.resolveNode(repositoryRoot)
	if err != nil || problem != nil {
		return facts, problem, nodeTool, err
	}
	if node.Origin != safeexec.ExternalOrigin {
		return facts, unavailable(
			"jest/node-repository-controlled", "the selected Node executable is repository-controlled", node.Path,
			"select and approve a Node executable outside the inspected repository",
		), nodeTool, nil
	}
	if err := validateNodeExecutable(node.Path, runtime.GOOS); err != nil {
		return facts, unavailable(
			"jest/node-not-direct", "the approved Node target is not a recognized native executable named node", err.Error(),
			"select the actual Node binary with --tool node=/absolute/path and run awareof --setup",
		), nodeTool, nil
	}

	repositoryPhysical, err := physicalRepositoryRoot(repositoryRoot)
	if err != nil {
		return nil, nil, "", err
	}
	contextRoot := repositoryPhysical
	if current.root != "." {
		contextRoot = filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.root)))
	}
	resolvedContext, err := filepath.EvalSymlinks(contextRoot)
	if err != nil {
		return facts, unavailable(
			"jest/context-uninspectable", "the Jest context root cannot be resolved safely", err.Error(),
			"repair the repository context or accept UNKNOWN",
		), "", nil
	}
	if !pathutil.Within(repositoryPhysical, resolvedContext) {
		return facts, unavailable(
			"jest/context-outside-repository", "the Jest context root resolves outside the repository", resolvedContext,
			"use a repository-contained Jest context or accept UNKNOWN",
		), "", nil
	}

	configArgument := config.argument
	if configArgument == "" {
		temporary, err := os.MkdirTemp("", "awareof-jest-")
		if err != nil {
			return nil, nil, "", fmt.Errorf("create Jest evaluator directory: %w", err)
		}
		defer func() {
			if err := os.RemoveAll(temporary); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove Jest evaluator directory: %w", err))
			}
		}()
		configArgument = filepath.Join(temporary, "jest.config.json")
		content, err := json.Marshal(map[string]string{"rootDir": resolvedContext})
		if err != nil {
			return nil, nil, "", fmt.Errorf("encode default Jest configuration: %w", err)
		}
		if err := os.WriteFile(configArgument, content, 0o600); err != nil {
			return nil, nil, "", fmt.Errorf("write default Jest configuration: %w", err)
		}
	}

	response, err := p.runner.Run(ctx, safeexec.Request{
		Root: repositoryRoot,
		Dir:  resolvedContext,
		Tool: nodeTool,
		Args: []string{
			installation.entry,
			"--config", configArgument,
			"--listTests",
			"--json",
			"--no-cache",
			"--no-watchman",
		},
		ExternalOnly: true,
		UnsetEnv:     nodeUnsetEnvironment,
	})
	if err != nil {
		if problem, ok := safeexec.AsUnavailable(err); ok {
			return facts, &scope.Explanation{
				Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
			}, problem.Tool, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, "", fmt.Errorf("run Jest test discovery: %w", ctxErr)
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return facts, unavailable(
				"command/timeout", "the delegated Jest discovery exceeded its time limit", err.Error(),
				"run Jest directly to diagnose slow repository configuration, then retry",
			), nodeTool, nil
		case errors.Is(err, safeexec.ErrOutputLimit):
			return facts, unavailable(
				"command/output-limit", "the delegated Jest discovery exceeded its output limit", err.Error(),
				"reduce repository configuration output, then retry",
			), nodeTool, nil
		default:
			return nil, nil, "", fmt.Errorf("run Jest test discovery: %w", err)
		}
	}
	if response.ExitCode != 0 {
		return facts, unavailable(
			"jest/evaluator-failed", "the delegated Jest discovery did not complete successfully",
			commandEvidence(response), "run Jest directly to diagnose the repository configuration, then retry",
		), nodeTool, nil
	}
	included, err := parseListTests(response.Stdout, repositoryPhysical)
	if err != nil {
		return facts, unavailable(
			"jest/evaluator-output-invalid", "the delegated Jest discovery returned an invalid result", err.Error(),
			"run Jest directly to diagnose repository output or side effects, then retry",
		), nodeTool, nil
	}
	for _, name := range paths {
		key := canonicalPathKey(filepath.Join(repositoryPhysical, filepath.FromSlash(string(name))))
		if _, exists := included[key]; exists {
			facts[name] = membershipFact{
				in: true, summary: "selected as an effective Jest test file", evidence: config.evidence, method: scope.SafeNative,
			}
			continue
		}
		facts[name] = membershipFact{
			summary: "not selected by effective Jest test discovery", evidence: config.evidence, method: scope.SafeNative,
		}
	}
	return facts, nil, "", nil
}

func preflightPaths(repositoryRoot string, paths []scope.Path) (map[scope.Path]membershipFact, []scope.Path) {
	facts := make(map[scope.Path]membershipFact, len(paths))
	evaluable := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if err := rejectSymlinkPath(repositoryRoot, string(name)); err != nil {
			facts[name] = unknownFact(scope.Explanation{
				Code: "jest/path-uninspectable", Summary: "the path cannot be mapped safely for Jest discovery", Evidence: err.Error(),
			})
			continue
		}
		absolute := filepath.Join(repositoryRoot, filepath.FromSlash(string(name)))
		info, err := os.Lstat(absolute)
		if err == nil && info.IsDir() {
			facts[name] = membershipFact{summary: "directories are not Jest test-file targets", method: scope.SafeParser}
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			facts[name] = unknownFact(scope.Explanation{
				Code: "jest/path-uninspectable", Summary: "the path cannot be inspected safely for Jest discovery", Evidence: err.Error(),
			})
			continue
		}
		evaluable = append(evaluable, name)
	}
	return facts, evaluable
}

func (p *Provider) resolveNode(root string) (safeexec.Target, *scope.Explanation, error) {
	target, err := p.resolver.Resolve(root, nodeTool)
	if err == nil {
		return target, nil, nil
	}
	if problem, ok := safeexec.AsUnavailable(err); ok {
		return safeexec.Target{}, &scope.Explanation{
			Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
		}, nil
	}
	return safeexec.Target{}, nil, fmt.Errorf("resolve Node: %w", err)
}

func parseListTests(content []byte, repositoryRoot string) (map[string]struct{}, error) {
	var paths []string
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&paths); err != nil {
		return nil, fmt.Errorf("decode test list: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("test list contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing test-list output: %w", err)
	}
	result := make(map[string]struct{}, len(paths))
	for _, name := range paths {
		if name == "" || !filepath.IsAbs(name) {
			return nil, fmt.Errorf("test list contains non-absolute path %q", name)
		}
		clean, err := filepath.EvalSymlinks(filepath.Clean(name))
		if err != nil {
			return nil, fmt.Errorf("resolve listed test path %q: %w", name, err)
		}
		if !pathutil.Within(repositoryRoot, clean) {
			continue
		}
		key := canonicalPathKey(clean)
		result[key] = struct{}{}
	}
	return result, nil
}

func physicalRepositoryRoot(repositoryRoot string) (string, error) {
	absolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve physical repository root: %w", err)
	}
	return physical, nil
}

func canonicalPathKey(name string) string {
	clean := filepath.Clean(name)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func validateNodeExecutable(name, goos string) error {
	base := strings.ToLower(filepath.Base(name))
	if (goos == "windows" && base != "node.exe") || (goos != "windows" && base != "node") {
		return fmt.Errorf("resolved path is %q", name)
	}
	file, err := os.Open(name) //nolint:gosec // name is the approved external target.
	if err != nil {
		return err
	}
	var header [4]byte
	_, readErr := io.ReadFull(file, header[:])
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	valid := false
	switch goos {
	case "linux":
		valid = bytes.Equal(header[:], []byte{0x7f, 'E', 'L', 'F'})
	case "darwin":
		valid = bytes.Equal(header[:], []byte{0xfe, 0xed, 0xfa, 0xce}) ||
			bytes.Equal(header[:], []byte{0xce, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(header[:], []byte{0xfe, 0xed, 0xfa, 0xcf}) ||
			bytes.Equal(header[:], []byte{0xcf, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(header[:], []byte{0xca, 0xfe, 0xba, 0xbe}) ||
			bytes.Equal(header[:], []byte{0xbe, 0xba, 0xfe, 0xca})
	case "windows":
		valid = bytes.Equal(header[:2], []byte{'M', 'Z'})
	default:
		return fmt.Errorf("platform %q is not supported", goos)
	}
	if !valid {
		return fmt.Errorf("entry point is not a recognized native executable for %s", goos)
	}
	return nil
}

func rejectSymlinkPath(repositoryRoot, logical string) error {
	current := repositoryRoot
	for _, component := range strings.Split(filepath.FromSlash(logical), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect queried path %q: %w", logical, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("queried path %q contains a symbolic link", logical)
		}
	}
	return nil
}

func unknownFact(explanation scope.Explanation) membershipFact {
	return membershipFact{unavailable: &explanation}
}

func unavailable(code, summary, evidence, action string) *scope.Explanation {
	return &scope.Explanation{Code: code, Summary: summary, Evidence: evidence, Action: action}
}

func commandEvidence(response safeexec.Response) string {
	detail := strings.TrimSpace(strings.ToValidUTF8(string(response.Stderr), "\uFFFD"))
	if detail == "" {
		detail = strings.TrimSpace(strings.ToValidUTF8(string(response.Stdout), "\uFFFD"))
	}
	if detail == "" {
		return fmt.Sprintf("evaluator exited with status %d", response.ExitCode)
	}
	return truncateUTF8(fmt.Sprintf("evaluator exited with status %d: %s", response.ExitCode, detail), 2048)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
