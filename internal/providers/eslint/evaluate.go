package eslint

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
	"ESLINT_FLAGS",
	"ESLINT_USE_FLAT_CONFIG",
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

type evaluatorRequest struct {
	ESLintEntry string   `json:"eslintEntry"`
	Config      string   `json:"config"`
	CWD         string   `json:"cwd"`
	Paths       []string `json:"paths"`
}

type evaluatorResponse struct {
	Results []wireEvaluatorResult `json:"results"`
}

type wireEvaluatorResult struct {
	Index       *int                  `json:"index"`
	Ignored     bool                  `json:"ignored"`
	Configured  bool                  `json:"configured"`
	Unavailable *evaluatorUnavailable `json:"unavailable,omitempty"`
}

type evaluatorUnavailable struct {
	Code     string `json:"code,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type evaluatorResult struct {
	Ignored     bool
	Configured  bool
	Unavailable *scope.Explanation
}

func (p *Provider) evaluateContext(
	ctx context.Context,
	repositoryRoot string,
	current eslintContext,
	paths []scope.Path,
) (_ map[scope.Path]eligibilityFact, _ *scope.Explanation, _ safeexec.ToolID, returnErr error) {
	facts := make(map[scope.Path]eligibilityFact, len(paths))
	if p.runner == nil {
		return nil, nil, "", errors.New("eslint command runner is nil")
	}
	if p.resolver == nil {
		return nil, nil, "", errors.New("eslint tool resolver is nil")
	}

	installation, err := loadProjectInstallation(repositoryRoot, current)
	if err != nil {
		return facts, unavailable(
			"eslint/package-unavailable", "the project ESLint installation could not be identified safely", err.Error(),
			"install a supported repository-local ESLint 9 or 10 package, then retry",
		), "", nil
	}
	node, problem, err := p.resolveNode(repositoryRoot)
	if err != nil || problem != nil {
		return facts, problem, nodeTool, err
	}
	if node.Origin != safeexec.ExternalOrigin {
		return facts, unavailable(
			"eslint/node-repository-controlled", "the selected Node executable is repository-controlled", node.Path,
			"select and approve a Node executable outside the inspected repository",
		), nodeTool, nil
	}
	if err := validateNodeExecutable(node.Path, runtime.GOOS); err != nil {
		return facts, unavailable(
			"eslint/node-not-direct", "the approved Node target is not a recognized native executable named node", err.Error(),
			"select the actual Node binary with --tool node=/absolute/path and run awareof --setup",
		), nodeTool, nil
	}

	repositoryPhysical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve physical repository root: %w", err)
	}
	contextRoot := repositoryPhysical
	if current.root != "." {
		contextRoot = filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.root)))
	}
	configPath := filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.config)))
	resolvedConfig, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return facts, unavailable(
			"eslint/config-uninspectable", "the selected ESLint configuration cannot be resolved safely", err.Error(),
			"repair the repository configuration or accept UNKNOWN",
		), "", nil
	}
	if !pathutil.Within(repositoryPhysical, resolvedConfig) {
		return facts, unavailable(
			"eslint/config-outside-repository", "the selected ESLint configuration resolves outside the repository", resolvedConfig,
			"use a repository-contained configuration or accept UNKNOWN",
		), "", nil
	}

	temporary, err := os.MkdirTemp("", "awareof-eslint-")
	if err != nil {
		return nil, nil, "", fmt.Errorf("create ESLint evaluator directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove ESLint evaluator directory: %w", err))
		}
	}()
	helperPath := filepath.Join(temporary, "evaluator.mjs")
	if err := os.WriteFile(helperPath, []byte(eslintHelper), 0o600); err != nil {
		return nil, nil, "", fmt.Errorf("write ESLint evaluator helper: %w", err)
	}
	request := evaluatorRequest{
		ESLintEntry: installation.entry, Config: resolvedConfig, CWD: contextRoot,
		Paths: make([]string, len(paths)),
	}
	for index, name := range paths {
		request.Paths[index] = filepath.Join(repositoryPhysical, filepath.FromSlash(string(name)))
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, nil, "", fmt.Errorf("encode ESLint evaluator request: %w", err)
	}
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root: repositoryRoot, Dir: contextRoot, Tool: nodeTool, ExternalOnly: true,
		Args: []string{helperPath}, Stdin: payload,
		UnsetEnv: nodeUnsetEnvironment,
	})
	if err != nil {
		if problem, ok := safeexec.AsUnavailable(err); ok {
			return facts, &scope.Explanation{
				Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
			}, problem.Tool, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, "", fmt.Errorf("run ESLint evaluator: %w", ctxErr)
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return facts, unavailable(
				"command/timeout", "the delegated ESLint evaluator exceeded its time limit", err.Error(),
				"run ESLint directly to diagnose slow repository configuration, then retry",
			), nodeTool, nil
		case errors.Is(err, safeexec.ErrOutputLimit):
			return facts, unavailable(
				"command/output-limit", "the delegated ESLint evaluator exceeded its output limit", err.Error(),
				"reduce repository configuration output, then retry",
			), nodeTool, nil
		default:
			return nil, nil, "", fmt.Errorf("run ESLint evaluator: %w", err)
		}
	}
	if response.ExitCode != 0 {
		return facts, unavailable(
			"eslint/evaluator-failed", "the delegated ESLint evaluator did not complete successfully",
			commandEvidence(response), "run ESLint directly to diagnose the repository configuration, then retry",
		), nodeTool, nil
	}
	parsed, err := parseEvaluatorResponse(response.Stdout, len(paths))
	if err != nil {
		return facts, unavailable(
			"eslint/evaluator-output-invalid", "the delegated ESLint evaluator returned an invalid result", err.Error(),
			"run ESLint directly to diagnose repository output or side effects, then retry",
		), nodeTool, nil
	}
	for index, name := range paths {
		result := parsed[index]
		if result.Unavailable != nil {
			facts[name] = eligibilityFact{unavailable: result.Unavailable, tool: nodeTool}
			continue
		}
		if result.Ignored {
			facts[name] = eligibilityFact{
				code: "eslint/ignored", summary: "ignored by effective ESLint configuration",
				config: current.config, method: scope.SafeNative,
			}
			continue
		}
		if !result.Configured {
			facts[name] = eligibilityFact{
				code: "eslint/unconfigured", summary: "no effective ESLint configuration selects the path",
				config: current.config, method: scope.SafeNative,
			}
			continue
		}
		facts[name] = eligibilityFact{
			in: true, code: "eslint/selected", summary: "selected by effective ESLint configuration",
			config: current.config, method: scope.SafeNative,
		}
	}
	return facts, nil, "", nil
}

func preflightPaths(
	repositoryRoot string,
	current eslintContext,
	paths []scope.Path,
) (map[scope.Path]eligibilityFact, []scope.Path) {
	facts := make(map[scope.Path]eligibilityFact, len(paths))
	evaluable := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if config, nested := nestedConfigFor(current, name); nested {
			facts[name] = unknownFact(scope.Explanation{
				Code:     "eslint/nested-config-unsupported",
				Summary:  "nested ESLint configuration cannot be bound with proven native-equivalent semantics",
				Evidence: string(config), Action: "evaluate this path with ESLint directly or accept UNKNOWN",
			})
			continue
		}
		if err := rejectSymlinkPath(repositoryRoot, string(name)); err != nil {
			facts[name] = unknownFact(scope.Explanation{
				Code: "eslint/path-uninspectable", Summary: "the path cannot be mapped safely for ESLint evaluation", Evidence: err.Error(),
			})
			continue
		}
		absolute := filepath.Join(repositoryRoot, filepath.FromSlash(string(name)))
		info, err := os.Lstat(absolute)
		if err == nil && info.IsDir() {
			facts[name] = eligibilityFact{
				code: "eslint/not-file", summary: "directories are not ESLint file targets", method: scope.SafeParser,
			}
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			facts[name] = unknownFact(scope.Explanation{
				Code: "eslint/path-uninspectable", Summary: "the path cannot be inspected safely for ESLint evaluation", Evidence: err.Error(),
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

func parseEvaluatorResponse(content []byte, count int) ([]evaluatorResult, error) {
	var response evaluatorResponse
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("response contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing response: %w", err)
	}
	if len(response.Results) != count {
		return nil, fmt.Errorf("response contains %d results; expected %d", len(response.Results), count)
	}
	ordered := make([]evaluatorResult, count)
	seen := make([]bool, count)
	for _, result := range response.Results {
		if result.Index == nil {
			return nil, errors.New("response result is missing an index")
		}
		index := *result.Index
		if index < 0 || index >= count || seen[index] {
			return nil, fmt.Errorf("response contains invalid result index %d", index)
		}
		if result.Ignored && result.Configured {
			return nil, fmt.Errorf("response result %d is both ignored and configured", index)
		}
		if result.Unavailable != nil && (result.Ignored || result.Configured) {
			return nil, fmt.Errorf("response result %d combines a fact with unavailability", index)
		}
		var problem *scope.Explanation
		if result.Unavailable != nil {
			problem = unavailable(
				"eslint/evaluation-unavailable", "ESLint could not establish file eligibility",
				truncateUTF8(result.Unavailable.Evidence, 2048), "run ESLint directly to diagnose this path, then retry",
			)
		}
		seen[index] = true
		ordered[index] = evaluatorResult{Ignored: result.Ignored, Configured: result.Configured, Unavailable: problem}
	}
	return ordered, nil
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

func unknownFact(explanation scope.Explanation) eligibilityFact {
	return eligibilityFact{unavailable: &explanation}
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
