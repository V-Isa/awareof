package prettier

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
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

var nodeUnsetEnvironment = []string{
	"BABEL_ENV",
	"FORCE_COLOR",
	"NODE_COMPILE_CACHE",
	"NODE_COMPILE_CACHE_PORTABLE",
	"NODE_COMPILE_CACHE_READONLY",
	"NODE_EXTRA_CA_CERTS",
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
	"NODE_REPL_EXTERNAL_MODULE",
	"NODE_V8_COVERAGE",
	"VSCODE_INSPECTOR_OPTIONS",
}

type evaluatorResponse struct {
	Results []evaluatorResult `json:"results"`
}

type evaluatorResult struct {
	Index       int                `json:"index"`
	Ignored     bool               `json:"ignored"`
	Parser      string             `json:"parser"`
	Config      string             `json:"config"`
	Unavailable *scope.Explanation `json:"unavailable"`
}

func (p *Provider) evaluateContext(
	ctx context.Context,
	repositoryRoot string,
	current prettierContext,
	paths []scope.Path,
) (_ map[scope.Path]eligibilityFact, _ *scope.Explanation, _ safeexec.ToolID, returnErr error) {
	facts := make(map[scope.Path]eligibilityFact, len(paths))
	safePaths := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if strings.ContainsAny(string(name), "\r\n") {
			facts[name] = unresolvedFact("prettier/path-unrepresentable", "the path cannot be represented by the evaluator protocol", string(name))
			continue
		}
		if err := rejectSymlinkPath(repositoryRoot, string(name)); err != nil {
			facts[name] = unresolvedFact("prettier/path-uninspectable", "the path cannot be mapped safely into the evaluator snapshot", err.Error())
			continue
		}
		safePaths = append(safePaths, name)
	}
	if len(safePaths) == 0 {
		return facts, nil, "", nil
	}
	if current.unavailable != nil {
		return facts, current.unavailable, "", nil
	}

	project, err := loadProjectInstallation(repositoryRoot, current)
	if err != nil {
		return facts, packageUnavailable("project", err), prettierTool, nil
	}
	projectBefore, err := packageFingerprint(project)
	if err != nil {
		return facts, packageUnavailable("project", err), prettierTool, nil
	}
	evaluator, evaluatorBefore, problem, err := p.selectEvaluator(repositoryRoot, project, projectBefore)
	if err != nil || problem != nil {
		return facts, problem, prettierTool, err
	}

	nodeTarget, problem, err := p.resolveTarget(repositoryRoot, nodeTool)
	if err != nil || problem != nil {
		return facts, problem, nodeTool, err
	}
	if nodeTarget.Origin != safeexec.ExternalOrigin {
		return facts, unavailable(
			"prettier/node-repository-controlled", "the selected Node executable is repository-controlled", nodeTarget.Path,
			"select and approve a Node executable outside the inspected repository",
		), nodeTool, nil
	}
	if err := validateNodeExecutable(nodeTarget.Path, runtime.GOOS); err != nil {
		return facts, unavailable(
			"prettier/node-not-direct", "the approved Node target is not a direct native Node executable", err.Error(),
			"select the actual Node binary with --tool node=/absolute/path and run awareof --setup",
		), nodeTool, nil
	}

	snapshot, err := createSnapshot(repositoryRoot, current, safePaths, evaluator.entry, project.version)
	if err != nil {
		return facts, unavailable(
			"prettier/snapshot-unavailable", "the Prettier context could not be mirrored safely", err.Error(),
			"repair the unreadable or ambiguous repository path or configuration, then retry",
		), prettierTool, nil
	}
	defer func() {
		if err := snapshot.close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	payload, err := json.Marshal(snapshot.request)
	if err != nil {
		return nil, nil, "", fmt.Errorf("encode Prettier evaluator request: %w", err)
	}
	helperPath := filepath.Join(snapshot.cleanupRoot, "awareof-prettier-helper.mjs")
	if err := os.WriteFile(helperPath, []byte(prettierHelper), 0o600); err != nil {
		return nil, nil, "", fmt.Errorf("write Prettier evaluator helper: %w", err)
	}
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root: repositoryRoot, Dir: snapshot.root, Tool: nodeTool, ExternalOnly: true,
		Args: []string{helperPath}, Stdin: payload,
		Env: map[string]string{
			"NODE_DISABLE_COMPILE_CACHE": "1",
			"NODE_ENV":                   "production",
			"NO_COLOR":                   "1",
		},
		UnsetEnv: nodeUnsetEnvironment,
	})
	if err != nil {
		if problem, ok := safeexec.AsUnavailable(err); ok {
			return facts, &scope.Explanation{
				Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
			}, problem.Tool, nil
		}
		return nil, nil, "", fmt.Errorf("run Prettier evaluator: %w", err)
	}
	if response.ExitCode != 0 {
		return facts, unavailable(
			"prettier/evaluator-failed", "the approved Prettier evaluator did not complete successfully",
			commandEvidence(response), "repair the approved evaluator or select another exact matching installation",
		), prettierTool, nil
	}

	parsed, err := parseEvaluatorResponse(response.Stdout, len(safePaths))
	if err != nil {
		return facts, unavailable(
			"prettier/evaluator-output-invalid", "the Prettier evaluator returned an invalid result", err.Error(),
			"use an intact supported Prettier installation, then retry",
		), prettierTool, nil
	}
	if err := snapshot.unchanged(); err != nil {
		return facts, unavailable(
			"prettier/config-changed", "Prettier inputs changed during evaluation", err.Error(), "retry after repository changes stop",
		), prettierTool, nil
	}
	projectAfter, projectErr := packageFingerprint(project)
	evaluatorAfter, evaluatorErr := packageFingerprint(evaluator)
	if projectErr != nil || evaluatorErr != nil || projectAfter != projectBefore || evaluatorAfter != evaluatorBefore {
		evidence := "package identity changed"
		if projectErr != nil {
			evidence = "project: " + projectErr.Error()
		} else if evaluatorErr != nil {
			evidence = "evaluator: " + evaluatorErr.Error()
		}
		return facts, unavailable(
			"prettier/package-changed", "a Prettier package changed during evaluation", evidence, "retry after package changes stop",
		), prettierTool, nil
	}

	for index, name := range safePaths {
		result := parsed[index]
		if result.Unavailable != nil {
			facts[name] = eligibilityFact{unavailable: result.Unavailable}
			continue
		}
		if result.Ignored {
			facts[name] = eligibilityFact{
				code: "prettier/ignored", summary: "ignored by the Prettier context", config: result.Config,
			}
			continue
		}
		if result.Parser == "" {
			facts[name] = eligibilityFact{
				code: "prettier/unsupported", summary: "not ignored, but Prettier inferred no parser", config: result.Config,
			}
			continue
		}
		facts[name] = eligibilityFact{in: true, code: "prettier/eligible", config: result.Config, parser: result.Parser}
	}
	return facts, nil, "", nil
}

func (p *Provider) selectEvaluator(
	root string,
	project packageInstallation,
	projectIdentity string,
) (packageInstallation, string, *scope.Explanation, error) {
	resolver, ok := p.discoverer.(interface {
		Resolve(string, safeexec.ToolID) (safeexec.Target, error)
	})
	if !ok {
		return packageInstallation{}, "", nil, errors.New("tool discoverer does not enforce approval")
	}
	target, err := resolver.Resolve(root, prettierTool)
	if err == nil {
		evaluator, identity, problem := inspectEvaluatorTarget(target, project, projectIdentity)
		return evaluator, identity, problem, nil
	}
	problem, unavailableError := safeexec.AsUnavailable(err)
	if !unavailableError {
		return packageInstallation{}, "", nil, fmt.Errorf("resolve Prettier evaluator: %w", err)
	}
	if problem.Code != "tool/selection-required" {
		return packageInstallation{}, "", &scope.Explanation{
			Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
		}, nil
	}
	approved, ok := p.discoverer.(approvedTargetDiscoverer)
	if !ok {
		return packageInstallation{}, "", selectionRequired(project.version), nil
	}
	targets, err := approved.ApprovedTargets(root, prettierTool)
	if err != nil {
		return packageInstallation{}, "", nil, fmt.Errorf("list approved Prettier evaluators: %w", err)
	}
	slices.SortFunc(targets, func(left, right safeexec.Target) int {
		return strings.Compare(left.Path, right.Path)
	})
	for _, candidate := range targets {
		evaluator, identity, candidateProblem := inspectEvaluatorTarget(candidate, project, projectIdentity)
		if candidateProblem == nil {
			return evaluator, identity, nil, nil
		}
	}
	return packageInstallation{}, "", selectionRequired(project.version), nil
}

func inspectEvaluatorTarget(
	target safeexec.Target,
	project packageInstallation,
	projectIdentity string,
) (packageInstallation, string, *scope.Explanation) {
	if target.Origin != safeexec.ExternalOrigin {
		return packageInstallation{}, "", unavailable(
			"prettier/evaluator-repository-controlled", "the selected Prettier evaluator is repository-controlled", target.Path,
			"select and approve a matching Prettier package outside the inspected repository",
		)
	}
	evaluator, err := loadEvaluatorInstallation(target.Path)
	if err != nil {
		return packageInstallation{}, "", packageUnavailable("evaluator", err)
	}
	if project.version != evaluator.version {
		return packageInstallation{}, "", unavailable(
			"prettier/evaluator-version-mismatch", "the approved Prettier evaluator does not match the project version",
			fmt.Sprintf("project %s; evaluator %s", project.version, evaluator.version),
			"select and approve the exact project Prettier version outside the repository",
		)
	}
	identity, err := packageFingerprint(evaluator)
	if err != nil {
		return packageInstallation{}, "", packageUnavailable("evaluator", err)
	}
	if projectIdentity != identity {
		return packageInstallation{}, "", unavailable(
			"prettier/evaluator-identity-mismatch", "the approved Prettier evaluator does not match the project's exact package identity",
			fmt.Sprintf("project %s; evaluator %s", projectIdentity, identity),
			"use an intact external copy of the exact project Prettier package",
		)
	}
	return evaluator, identity, nil
}

func selectionRequired(version string) *scope.Explanation {
	return unavailable(
		"prettier/evaluator-selection-required", "no approved matching Prettier evaluator was found",
		"project uses Prettier "+version,
		"select its external index.mjs with --tool prettier=/absolute/path and run awareof --setup",
	)
}

func (p *Provider) resolveTarget(root string, tool safeexec.ToolID) (safeexec.Target, *scope.Explanation, error) {
	resolver, ok := p.discoverer.(interface {
		Resolve(string, safeexec.ToolID) (safeexec.Target, error)
	})
	if !ok {
		return safeexec.Target{}, nil, errors.New("tool discoverer does not enforce approval")
	}
	target, err := resolver.Resolve(root, tool)
	if err == nil {
		return target, nil, nil
	}
	if problem, ok := safeexec.AsUnavailable(err); ok {
		return safeexec.Target{}, &scope.Explanation{
			Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
		}, nil
	}
	return safeexec.Target{}, nil, fmt.Errorf("resolve %s: %w", tool, err)
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
			return nil, errors.New("decode response: multiple JSON values")
		}
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(response.Results) != count {
		return nil, fmt.Errorf("response contains %d results; expected %d", len(response.Results), count)
	}
	ordered := make([]evaluatorResult, count)
	seen := make([]bool, count)
	for _, result := range response.Results {
		if result.Index < 0 || result.Index >= count || seen[result.Index] {
			return nil, fmt.Errorf("response contains invalid result index %d", result.Index)
		}
		if result.Unavailable != nil && !result.Unavailable.Valid() {
			return nil, fmt.Errorf("response result %d has invalid explanation", result.Index)
		}
		seen[result.Index] = true
		ordered[result.Index] = result
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

func unresolvedFact(code, summary, evidence string) eligibilityFact {
	return eligibilityFact{unavailable: &scope.Explanation{Code: code, Summary: summary, Evidence: evidence}}
}

func unavailable(code, summary, evidence, action string) *scope.Explanation {
	return &scope.Explanation{Code: code, Summary: summary, Evidence: evidence, Action: action}
}

func packageUnavailable(subject string, err error) *scope.Explanation {
	return unavailable(
		"prettier/package-unavailable", "the exact Prettier package identity could not be established",
		subject+": "+err.Error(), "make an intact matching Prettier 3.x package available, then retry",
	)
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
