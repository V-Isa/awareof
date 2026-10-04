package typescript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

type projectEvaluation struct {
	included        map[scope.Path]struct{}
	references      []scope.Path
	unavailableTool safeexec.ToolID
}

var typeScriptUnsetEnvironment = []string{
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
	"TS_NODE_PROJECT",
	"VSCODE_INSPECTOR_OPTIONS",
}

func (p *Provider) evaluateProject(
	ctx context.Context,
	root string,
	config scope.Path,
	queries queryIndex,
) (projectEvaluation, *scope.Explanation, error) {
	projectCompiler, err := findProjectCompiler(root, config, p.platform())
	if err != nil {
		return projectEvaluation{}, unavailable(
			"typescript/project-compiler-unavailable",
			"the project's exact TypeScript compiler could not be identified safely",
			err.Error(),
			"make the project's exact TypeScript package available safely, or select a supported project root",
		), nil
	}

	target, unavailableTarget, err := p.selectEvaluator(root, projectCompiler)
	if err != nil {
		return projectEvaluation{}, nil, err
	}
	if unavailableTarget != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailableTarget, nil
	}
	selected, err := installationFromTarget(target, p.platform())
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/evaluator-unsupported",
			"the selected TypeScript evaluator is not a supported compiler core",
			err.Error(),
			"select the exact TypeScript 6 lib/_tsc.js or TypeScript 7 platform lib/tsc",
		), nil
	}
	if projectCompiler.major != selected.major || projectCompiler.version != selected.version {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/evaluator-version-mismatch",
			"the selected evaluator does not match the project's installed TypeScript version",
			fmt.Sprintf("project uses %s; evaluator is %s", projectCompiler.version, selected.version),
			"select an evaluator with the exact project version",
		), nil
	}
	if projectCompiler.major == 6 && target.Origin != safeexec.ExternalOrigin {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/repository-javascript-unsupported",
			"repository-local TypeScript 6 JavaScript is not executed",
			fmt.Sprintf("selected evaluator %q is repository-controlled", target.Path),
			"select the identical TypeScript 6 lib/_tsc.js from an external installation",
		), nil
	}

	projectBefore, err := packageFingerprint(projectCompiler)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, fingerprintUnavailable("project", err), nil
	}
	selectedBefore, err := packageFingerprint(selected)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, fingerprintUnavailable("selected evaluator", err), nil
	}
	if projectBefore != selectedBefore {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/evaluator-identity-mismatch",
			"the selected evaluator does not match the project's installed TypeScript compiler inputs",
			fmt.Sprintf("project fingerprint %s; evaluator fingerprint %s", projectBefore, selectedBefore),
			"select an unmodified evaluator from the exact same TypeScript package",
		), nil
	}

	request := safeexec.Request{
		Root:       root,
		Tool:       typescriptTool,
		EntryPoint: target.Path,
		Env: map[string]string{
			"NODE_DISABLE_COMPILE_CACHE": "1",
			"NODE_ENV":                   "production",
			"NO_COLOR":                   "1",
		},
		UnsetEnv: append([]string(nil), typeScriptUnsetEnvironment...),
	}
	if projectCompiler.major == 6 {
		request.Interpreter = nodeTool
		request.ExternalOnly = true
	}
	request.Args = []string{
		"-p", filepath.FromSlash(string(config)),
		"--showConfig",
		"--pretty", "false",
	}
	preflight, problem, problemTool, err := p.runEvaluator(ctx, request)
	if err != nil {
		return projectEvaluation{}, nil, err
	}
	if problem != nil {
		return projectEvaluation{unavailableTool: problemTool}, problem, nil
	}
	if preflight.ExitCode != 0 {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/config-preflight-failed",
			"TypeScript could not resolve the effective project configuration safely",
			commandEvidence(preflight),
			"repair the project configuration and retry",
		), nil
	}
	option, err := writeCausingCompilerOption(preflight.Stdout)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/config-preflight-ambiguous",
			"TypeScript's effective project configuration could not be interpreted safely",
			err.Error(),
			"review the compiler output in a trusted or isolated environment",
		), nil
	}
	if option != "" {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/write-causing-option-unsupported",
			"the effective TypeScript configuration can write files during inspection",
			fmt.Sprintf("compiler option %q is set", option),
			"use a project configuration without the write-causing option",
		), nil
	}

	request.Args = []string{
		"-p", filepath.FromSlash(string(config)),
		"--listFilesOnly",
		"--pretty", "false",
	}
	response, problem, problemTool, err := p.runEvaluator(ctx, request)
	if err != nil {
		return projectEvaluation{}, nil, err
	}
	if problem != nil {
		return projectEvaluation{unavailableTool: problemTool}, problem, nil
	}
	if response.ExitCode != 0 {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/compiler-failed",
			"TypeScript could not produce a complete Program file list",
			commandEvidence(response),
			"repair the project configuration and retry",
		), nil
	}

	projectAfter, err := packageFingerprint(projectCompiler)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, fingerprintUnavailable("project after evaluation", err), nil
	}
	selectedAfter, err := packageFingerprint(selected)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, fingerprintUnavailable("selected evaluator after evaluation", err), nil
	}
	if projectBefore != projectAfter || selectedBefore != selectedAfter || projectAfter != selectedAfter {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/evaluator-changed",
			"the TypeScript compiler inputs changed during evaluation",
			"the compiler result was discarded",
			"retry after repository and tool changes stop",
		), nil
	}

	included, err := parseListFiles(response.Stdout, queries)
	if err != nil {
		return projectEvaluation{unavailableTool: typescriptTool}, unavailable(
			"typescript/output-ambiguous",
			"TypeScript file-list output could not be interpreted safely",
			err.Error(),
			"review the compiler output in a trusted or isolated environment",
		), nil
	}
	references, err := readReferences(root, config)
	if err != nil {
		return projectEvaluation{included: included}, unavailable(
			"typescript/references-unresolved",
			"TypeScript project references could not be resolved safely",
			err.Error(),
			"repair the project references and retry",
		), nil
	}
	return projectEvaluation{included: included, references: references}, nil, nil
}

func (p *Provider) runEvaluator(
	ctx context.Context,
	request safeexec.Request,
) (safeexec.Response, *scope.Explanation, safeexec.ToolID, error) {
	response, err := p.runner.Run(ctx, request)
	if err == nil {
		return response, nil, "", nil
	}
	if problem, ok := safeexec.AsUnavailable(err); ok {
		return safeexec.Response{}, &scope.Explanation{
			Code:     problem.Code,
			Summary:  problem.Summary,
			Evidence: problem.Evidence,
			Action:   problem.Action,
		}, problem.Tool, nil
	}
	return safeexec.Response{}, nil, "", fmt.Errorf("run TypeScript evaluator: %w", err)
}

func writeCausingCompilerOption(content []byte) (string, error) {
	if !utf8.Valid(content) {
		return "", errors.New("effective configuration is not valid UTF-8")
	}
	var config struct {
		CompilerOptions map[string]json.RawMessage `json:"compilerOptions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&config); err != nil {
		return "", fmt.Errorf("parse effective configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("parse effective configuration: multiple JSON values")
		}
		return "", fmt.Errorf("parse effective configuration: %w", err)
	}
	if _, configured := config.CompilerOptions["generateTrace"]; configured {
		return "generateTrace", nil
	}
	return "", nil
}

func (p *Provider) selectEvaluator(root string, project compilerInstallation) (safeexec.Target, *scope.Explanation, error) {
	target, err := p.discoverer.Discover(root, typescriptTool)
	if err == nil {
		return target, nil, nil
	}
	problem, isUnavailable := safeexec.AsUnavailable(err)
	if !isUnavailable {
		return safeexec.Target{}, nil, fmt.Errorf("discover TypeScript evaluator: %w", err)
	}
	if problem.Code != "tool/selection-required" {
		return safeexec.Target{}, &scope.Explanation{
			Code: problem.Code, Summary: problem.Summary, Evidence: problem.Evidence, Action: problem.Action,
		}, nil
	}
	candidates, err := p.evaluatorCandidates(root, project)
	if err != nil {
		return safeexec.Target{}, nil, err
	}
	if len(candidates) != 0 {
		return candidates[0], nil, nil
	}
	return safeexec.Target{}, unavailable(
		"typescript/evaluator-selection-required",
		"no safe matching TypeScript evaluator was discovered",
		fmt.Sprintf("project uses TypeScript %s", project.version),
		"select a matching evaluator with --tool typescript=/absolute/path and run awareof --setup",
	), nil
}

func unavailable(code, summary, evidence, action string) *scope.Explanation {
	return &scope.Explanation{Code: code, Summary: summary, Evidence: evidence, Action: action}
}

func fingerprintUnavailable(subject string, err error) *scope.Explanation {
	return unavailable(
		"typescript/evaluator-fingerprint-unavailable",
		"the exact TypeScript compiler-input identity could not be established",
		subject+": "+err.Error(),
		"make an intact matching TypeScript package available, then retry",
	)
}

func commandEvidence(response safeexec.Response) string {
	detail := strings.TrimSpace(strings.ToValidUTF8(string(response.Stderr), "\uFFFD"))
	if detail == "" {
		detail = strings.TrimSpace(strings.ToValidUTF8(string(response.Stdout), "\uFFFD"))
	}
	detail = truncateText(detail, 2048)
	if detail == "" {
		return fmt.Sprintf("compiler exited with status %d", response.ExitCode)
	}
	return fmt.Sprintf("compiler exited with status %d: %s", response.ExitCode, detail)
}

type queryIndex struct {
	rootAbsolute string
	rootResolved string
	goos         string
	wanted       map[string][]scope.Path
}

func prepareQueryIndex(root string, paths []scope.Path, goos string) (queryIndex, map[scope.Path]scope.Explanation, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return queryIndex{}, nil, fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return queryIndex{}, nil, fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	index := queryIndex{
		rootAbsolute: rootAbsolute,
		rootResolved: rootResolved,
		goos:         goos,
		wanted:       make(map[string][]scope.Path, len(paths)),
	}
	unresolved := make(map[scope.Path]scope.Explanation)
	for _, name := range paths {
		if strings.ContainsAny(string(name), "\r\n") {
			unresolved[name] = scope.Explanation{
				Code:     "typescript/path-unrepresentable",
				Summary:  "the queried path cannot be represented safely in TypeScript's line-oriented output",
				Evidence: fmt.Sprintf("path %q contains a line break", name),
			}
			continue
		}
		symlink, err := pathContainsSymlink(rootAbsolute, name)
		if err != nil {
			unresolved[name] = scope.Explanation{
				Code:     "typescript/path-uninspectable",
				Summary:  "the queried path could not be inspected safely",
				Evidence: err.Error(),
			}
			continue
		}
		if symlink {
			unresolved[name] = scope.Explanation{
				Code:     "typescript/path-symlink-ambiguous",
				Summary:  "a queried symlink cannot be mapped to TypeScript's logical file identity safely",
				Evidence: string(name),
			}
			continue
		}
		absolute := filepath.Join(rootAbsolute, filepath.FromSlash(string(name)))
		key := canonicalFileName(absolute, goos)
		index.wanted[key] = append(index.wanted[key], name)
	}
	return index, unresolved, nil
}

func pathContainsSymlink(root string, name scope.Path) (bool, error) {
	current := root
	for _, part := range strings.Split(filepath.FromSlash(string(name)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect queried path %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

func parseListFiles(content []byte, queries queryIndex) (map[scope.Path]struct{}, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("output is not valid UTF-8")
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("output contains a NUL byte")
	}
	included := make(map[scope.Path]struct{}, len(queries.wanted))
	if len(content) == 0 {
		return included, nil
	}
	if content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	for _, record := range bytes.Split(content, []byte{'\n'}) {
		line := strings.TrimSuffix(string(record), "\r")
		if line == "" {
			return nil, errors.New("output contains an empty file-name record")
		}
		if !filepath.IsAbs(line) {
			return nil, fmt.Errorf("output path %q is not absolute", line)
		}
		clean := filepath.Clean(line)
		if names := queries.wanted[canonicalFileName(clean, queries.goos)]; len(names) != 0 {
			for _, name := range names {
				included[name] = struct{}{}
			}
			continue
		}
		if queries.rootResolved != queries.rootAbsolute {
			if relative, err := filepath.Rel(queries.rootResolved, clean); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				logical := filepath.Join(queries.rootAbsolute, relative)
				for _, name := range queries.wanted[canonicalFileName(logical, queries.goos)] {
					included[name] = struct{}{}
				}
			}
		}
	}
	return included, nil
}

func canonicalFileName(name, goos string) string {
	clean := filepath.Clean(name)
	if goos == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func sortedPaths(set map[scope.Path]struct{}) []scope.Path {
	paths := make([]scope.Path, 0, len(set))
	for name := range set {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	return paths
}
