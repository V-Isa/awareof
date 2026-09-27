package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/V-Isa/awareof/internal/contract"
	"github.com/V-Isa/awareof/internal/diagnostic"
	"github.com/V-Isa/awareof/internal/pathset"
	"github.com/V-Isa/awareof/internal/pathsource"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/providers/codeowners"
	"github.com/V-Isa/awareof/internal/providers/docker"
	"github.com/V-Isa/awareof/internal/providers/eas"
	"github.com/V-Isa/awareof/internal/providers/git"
	"github.com/V-Isa/awareof/internal/providers/npm"
	"github.com/V-Isa/awareof/internal/render"
	"github.com/V-Isa/awareof/internal/repository"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const helpText = `See which tools include, exclude, or cover each repository file.

Usage:
  awareof [options] [PATH...]

Common workflows:
  awareof --setup                 review and approve native tools
  awareof .env                    inspect one path
  awareof src/                    inspect a directory recursively
  awareof --validate              validate the repository contract
  awareof --staged --validate     validate staged paths
  awareof --changed-from main     inspect paths changed from main

Paths:
  file.ts                  exact path
  src/                     recursive directory
  '**/*.generated.ts'      awareof glob
  --literal 'name[1].ts'   exact path that contains glob syntax
  -                        paths from stdin

Options:
  --setup                  discover and approve native tools safely
  --validate               validate the repository contract
  --staged                 inspect paths staged in Git
  --changed-from REF       inspect tracked paths changed from Git revision REF
  --literal                treat positional paths as literal, not globs
  --json                   write JSON
  -q, --quiet              suppress validation output
  -C, --root DIR           use DIR as the repository root
  -0, --null-input         read NUL-delimited paths from stdin
  -h, --help               show help
  --version                show version

Native-tool options:
  --tools                  show native-tool approval status
  --tool ID=PATH           select an executable for this invocation
  --approve-tool ID        approve the resolved executable; repeatable
  --revoke-tool ID         revoke all approvals for a tool; repeatable
`

var Version = "dev"

type options struct {
	json        bool
	validate    bool
	quiet       bool
	nullInput   bool
	literal     bool
	root        string
	help        bool
	version     bool
	setup       bool
	tools       bool
	staged      bool
	changedFrom string
	toolPaths   map[safeexec.ToolID]string
	approve     []safeexec.ToolID
	revoke      []safeexec.ToolID
	paths       []string
}

type toolManager interface {
	SetOverrides(map[safeexec.ToolID]string) error
	Approve(string, safeexec.ToolID) (safeexec.Target, error)
	ApproveTarget(string, safeexec.Target) (safeexec.Target, error)
	Revoke(string, safeexec.ToolID) (int, error)
	Statuses(string) []safeexec.Status
	StatusesFor(string, []safeexec.ToolID) []safeexec.Status
}

type changeSource interface {
	Staged(context.Context, string) ([]scope.Path, error)
	ChangedFrom(context.Context, string, string) ([]scope.Path, error)
}

type dependencies struct {
	getwd        func() (string, error)
	resolveRoot  func(string, string) (string, error)
	buildPaths   func(context.Context, pathset.Builder, []string, io.Reader, pathset.BuildOptions) ([]scope.Path, error)
	loadContract func(string, []scope.ProviderID) (contract.Contract, error)
	setupTools   func(context.Context, string) ([]safeexec.ToolID, error)
	providers    []provider.Provider
	tools        toolManager
	changes      changeSource
	interactive  func(io.Reader, io.Writer, io.Writer) bool
}

// Run executes the CLI and returns a process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	approvalPath, approvalPathErr := safeexec.DefaultApprovalPath()
	var approvalStore safeexec.ApprovalStore = safeexec.UnavailableApprovalStore{Cause: approvalPathErr}
	if approvalPathErr == nil {
		approvalStore = safeexec.FileApprovalStore{Path: approvalPath}
	}
	deps, err := defaultDependencies(approvalStore)
	if err != nil {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "tool/configuration", Summary: "cannot configure native tools", Evidence: err.Error()})
		return 2
	}
	return run(ctx, args, stdin, stdout, stderr, deps)
}

func defaultDependencies(approvalStore safeexec.ApprovalStore) (dependencies, error) {
	manager, err := safeexec.NewManager([]safeexec.Tool{
		{ID: "git", Command: "git"},
		{ID: "node", Command: "node"},
		{ID: "npm", Command: "npm"},
	}, approvalStore)
	if err != nil {
		return dependencies{}, err
	}
	runner := safeexec.Runner{Resolver: manager}
	gitProvider := git.New(runner)
	easProvider := eas.New(runner)
	npmProvider := npm.New(runner)
	return dependencies{
		getwd:       os.Getwd,
		resolveRoot: repository.ResolveRoot,
		buildPaths: func(
			ctx context.Context,
			builder pathset.Builder,
			inputs []string,
			input io.Reader,
			options pathset.BuildOptions,
		) ([]scope.Path, error) {
			return builder.Build(ctx, inputs, input, options)
		},
		loadContract: contract.Load,
		setupTools: func(ctx context.Context, root string) ([]safeexec.ToolID, error) {
			return relevantNativeTools(ctx, root, nativeProviders{
				git: gitProvider,
				npm: npmProvider,
			})
		},
		providers:   []provider.Provider{codeowners.New(), docker.New(), easProvider, gitProvider, npmProvider},
		tools:       manager,
		changes:     pathsource.NewGit(runner),
		interactive: terminalInteraction,
	}, nil
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	deps dependencies,
) int {
	opts, err := parseOptions(args)
	if err != nil {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: err.Error(), Action: "run 'awareof --help'"})
		return 2
	}
	if opts.help {
		if _, err := io.WriteString(stdout, helpText); err != nil {
			writeError(stderr, "output/write", "cannot write help", err.Error())
			return 2
		}
		return 0
	}
	if opts.version {
		if _, err := fmt.Fprintf(stdout, "awareof %s\n", Version); err != nil {
			writeError(stderr, "output/write", "cannot write version", err.Error())
			return 2
		}
		return 0
	}
	if opts.quiet && !opts.validate {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--quiet requires --validate", Action: "run 'awareof --help'"})
		return 2
	}
	managesTools := opts.setup || opts.tools || len(opts.approve) > 0 || len(opts.revoke) > 0
	hasChangeSource := opts.staged || opts.changedFrom != ""
	hasPathRequest := len(opts.paths) > 0 || hasChangeSource || opts.nullInput || opts.literal
	hasOutputOptions := opts.quiet || opts.json
	hasInspectionOptions := opts.validate || hasPathRequest || hasOutputOptions
	if managesTools && hasInspectionOptions {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "tool management cannot be combined with inspection, validation, or JSON output", Action: "run 'awareof --help'"})
		return 2
	}
	if len(opts.approve) > 0 && len(opts.revoke) > 0 {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--approve-tool and --revoke-tool cannot be combined", Action: "run separate commands"})
		return 2
	}
	if opts.setup && (opts.tools || len(opts.approve) > 0 || len(opts.revoke) > 0) {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--setup cannot be combined with other native-tool operations", Action: "run separate commands"})
		return 2
	}
	if opts.staged && opts.changedFrom != "" {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--staged and --changed-from cannot be combined", Action: "choose one path source"})
		return 2
	}
	if hasChangeSource && len(opts.paths) > 0 {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "Git change selectors cannot be combined with positional paths or stdin", Action: "choose one path source"})
		return 2
	}
	if hasChangeSource && opts.nullInput {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--null-input requires the stdin path source", Action: "remove --null-input or use '-'"})
		return 2
	}
	if hasChangeSource && opts.literal {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--literal requires positional paths", Action: "remove --literal or choose a positional path source"})
		return 2
	}
	if opts.literal && len(opts.paths) == 0 {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "--literal requires at least one positional path", Action: "provide a path or remove --literal"})
		return 2
	}
	if len(opts.paths) == 0 && !opts.validate && !managesTools && !hasChangeSource {
		writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: "cli/usage", Summary: "missing path or pattern", Action: "run 'awareof --help'"})
		return 2
	}

	cwd, err := deps.getwd()
	if err != nil {
		writeError(stderr, "repository/working-directory", "cannot get the working directory", err.Error())
		return 2
	}
	root, err := deps.resolveRoot(cwd, opts.root)
	if err != nil {
		writeError(stderr, "repository/root", "cannot resolve the repository root", err.Error())
		return 2
	}
	base := cwd
	if opts.root != "" {
		base = root
	}
	if deps.tools != nil {
		if err := deps.tools.SetOverrides(opts.toolPaths); err != nil {
			writeError(stderr, "tool/configuration", "cannot configure native tools", err.Error())
			return 2
		}
	} else if len(opts.toolPaths) > 0 || managesTools {
		writeError(stderr, "tool/configuration", "native-tool management is unavailable", "the application has no tool manager")
		return 2
	}
	if managesTools {
		if opts.setup {
			var statuses []safeexec.Status
			if deps.setupTools != nil {
				relevant, setupErr := deps.setupTools(ctx, root)
				if setupErr != nil {
					writeError(stderr, "setup/discovery", "cannot determine relevant native tools", setupErr.Error())
					return 2
				}
				statuses = deps.tools.StatusesFor(root, relevant)
			} else {
				statuses = deps.tools.Statuses(root)
			}
			interactive := deps.interactive != nil && deps.interactive(stdin, stdout, stderr)
			return runSetup(root, stdin, stdout, stderr, deps.tools, statuses, interactive)
		}
		return runToolManagement(root, stdout, stderr, opts, deps.tools)
	}
	inputs := opts.paths
	var repositoryContract contract.Contract
	if opts.validate {
		providerIDs, idsErr := provider.IDs(deps.providers)
		if idsErr != nil {
			writeError(stderr, "provider/configuration", "cannot prepare providers", idsErr.Error())
			return 2
		}
		repositoryContract, err = deps.loadContract(root, providerIDs)
		if err != nil {
			writeError(stderr, "contract/load", "cannot load the repository contract", err.Error())
			return 2
		}
		if len(inputs) == 0 && !hasChangeSource {
			inputs = []string{"**"}
			base = root
		}
	}
	var paths []scope.Path
	switch {
	case opts.staged:
		if deps.changes == nil {
			err = errors.New("git change path source is unavailable")
		} else {
			paths, err = deps.changes.Staged(ctx, root)
		}
	case opts.changedFrom != "":
		if deps.changes == nil {
			err = errors.New("git change path source is unavailable")
		} else {
			paths, err = deps.changes.ChangedFrom(ctx, root, opts.changedFrom)
		}
	default:
		paths, err = deps.buildPaths(
			ctx,
			pathset.Builder{Root: root, Base: base},
			inputs,
			stdin,
			pathset.BuildOptions{Literal: opts.literal, NullInput: opts.nullInput},
		)
	}
	if err != nil {
		if unavailable, ok := safeexec.AsUnavailable(err); ok {
			writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: unavailable.Code, Summary: unavailable.Summary, Evidence: unavailable.Evidence, Action: unavailable.Action})
		} else if hasChangeSource {
			writeError(stderr, "pathset/source", "cannot build the path set from Git changes", err.Error())
		} else {
			writeError(stderr, "pathset/build", "cannot build the path set", err.Error())
		}
		return 2
	}
	if opts.validate {
		paths, err = repositoryContract.MatchingPaths(paths)
		if err != nil {
			writeError(stderr, "contract/path-selection", "cannot select contract paths", err.Error())
			return 2
		}
	}
	if opts.validate && len(paths) == 0 {
		report := contract.Report{Contract: repositoryContract.File, Checks: []contract.Check{}}
		if !opts.quiet {
			if opts.json {
				err = render.ValidationJSON(stdout, report)
			} else {
				err = render.ValidationHuman(stdout, report)
			}
			if err != nil {
				writeError(stderr, "output/write", "cannot write validation output", err.Error())
				return 2
			}
		}
		return 0
	}
	evaluationProviders := deps.providers
	if opts.validate {
		requiredProviders, requiredErr := repositoryContract.RequiredProviders(paths)
		if requiredErr != nil {
			writeError(stderr, "contract/prepare", "cannot prepare contract validation", requiredErr.Error())
			return 2
		}
		evaluationProviders, err = provider.Select(deps.providers, requiredProviders)
		if err != nil {
			writeError(stderr, "provider/selection", "cannot select providers", err.Error())
			return 2
		}
	}
	results, err := provider.Evaluate(ctx, provider.Repository{Root: root}, paths, evaluationProviders)
	if err != nil {
		writeError(stderr, "provider/evaluation", "provider evaluation failed", err.Error())
		return 2
	}

	if opts.validate {
		report, validationErr := repositoryContract.Evaluate(paths, results)
		if validationErr != nil {
			writeError(stderr, "contract/evaluation", "contract evaluation failed", validationErr.Error())
			return 2
		}
		if !opts.quiet {
			if opts.json {
				err = render.ValidationJSON(stdout, report)
			} else {
				err = render.ValidationHuman(stdout, report)
			}
			if err != nil {
				writeError(stderr, "output/write", "cannot write validation output", err.Error())
				return 2
			}
		}
		if !report.Valid() {
			return 1
		}
		return 0
	}
	if opts.json {
		err = render.JSON(stdout, paths, results)
	} else {
		err = render.Human(stdout, paths, results)
	}
	if err != nil {
		writeError(stderr, "output/write", "cannot write inspection output", err.Error())
		return 2
	}
	return 0
}

func parseOptions(args []string) (options, error) {
	var opts options
	parseFlags := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parseFlags && arg == "--" {
			parseFlags = false
			continue
		}
		if !parseFlags || arg == "-" || !strings.HasPrefix(arg, "-") {
			opts.paths = append(opts.paths, arg)
			continue
		}

		switch {
		case arg == "--json":
			opts.json = true
		case arg == "--validate":
			opts.validate = true
		case arg == "--staged":
			opts.staged = true
		case arg == "--changed-from":
			if i+1 == len(args) {
				return options{}, errors.New("missing value for --changed-from")
			}
			i++
			if err := setChangedFrom(&opts, args[i]); err != nil {
				return options{}, err
			}
		case strings.HasPrefix(arg, "--changed-from="):
			if err := setChangedFrom(&opts, strings.TrimPrefix(arg, "--changed-from=")); err != nil {
				return options{}, err
			}
		case arg == "--tools":
			opts.tools = true
		case arg == "--setup":
			opts.setup = true
		case arg == "--tool":
			if i+1 == len(args) {
				return options{}, errors.New("missing value for --tool")
			}
			i++
			if err := addToolOverride(&opts, args[i]); err != nil {
				return options{}, err
			}
		case strings.HasPrefix(arg, "--tool="):
			if err := addToolOverride(&opts, strings.TrimPrefix(arg, "--tool=")); err != nil {
				return options{}, err
			}
		case arg == "--approve-tool":
			if i+1 == len(args) {
				return options{}, errors.New("missing value for --approve-tool")
			}
			i++
			if err := addToolID(&opts.approve, args[i], "--approve-tool"); err != nil {
				return options{}, err
			}
		case strings.HasPrefix(arg, "--approve-tool="):
			value := strings.TrimPrefix(arg, "--approve-tool=")
			if value == "" {
				return options{}, errors.New("missing value for --approve-tool")
			}
			if err := addToolID(&opts.approve, value, "--approve-tool"); err != nil {
				return options{}, err
			}
		case arg == "--revoke-tool":
			if i+1 == len(args) {
				return options{}, errors.New("missing value for --revoke-tool")
			}
			i++
			if err := addToolID(&opts.revoke, args[i], "--revoke-tool"); err != nil {
				return options{}, err
			}
		case strings.HasPrefix(arg, "--revoke-tool="):
			value := strings.TrimPrefix(arg, "--revoke-tool=")
			if value == "" {
				return options{}, errors.New("missing value for --revoke-tool")
			}
			if err := addToolID(&opts.revoke, value, "--revoke-tool"); err != nil {
				return options{}, err
			}
		case arg == "-q" || arg == "--quiet":
			opts.quiet = true
		case arg == "-0" || arg == "--null-input":
			opts.nullInput = true
		case arg == "--literal":
			opts.literal = true
		case arg == "-h" || arg == "--help":
			opts.help = true
		case arg == "--version":
			opts.version = true
		case arg == "-C" || arg == "--root":
			if i+1 == len(args) {
				return options{}, errors.New("missing value for --root")
			}
			i++
			opts.root = args[i]
			if opts.root == "" {
				return options{}, errors.New("missing value for --root")
			}
		case strings.HasPrefix(arg, "--root="):
			opts.root = strings.TrimPrefix(arg, "--root=")
			if opts.root == "" {
				return options{}, errors.New("missing value for --root")
			}
		default:
			return options{}, fmt.Errorf("unknown option %q", arg)
		}
	}
	return opts, nil
}

func setChangedFrom(opts *options, revision string) error {
	if revision == "" {
		return errors.New("missing value for --changed-from")
	}
	if opts.changedFrom != "" {
		return errors.New("--changed-from may be used only once")
	}
	opts.changedFrom = revision
	return nil
}

func addToolOverride(opts *options, value string) error {
	id, path, ok := strings.Cut(value, "=")
	if !ok || id == "" || path == "" {
		return errors.New("--tool requires ID=PATH")
	}
	toolID := safeexec.ToolID(id)
	if opts.toolPaths == nil {
		opts.toolPaths = make(map[safeexec.ToolID]string)
	}
	if _, exists := opts.toolPaths[toolID]; exists {
		return fmt.Errorf("tool %q is selected more than once", toolID)
	}
	opts.toolPaths[toolID] = path
	return nil
}

func addToolID(ids *[]safeexec.ToolID, value, option string) error {
	if value == "" {
		return fmt.Errorf("missing value for %s", option)
	}
	id := safeexec.ToolID(value)
	for _, existing := range *ids {
		if existing == id {
			return fmt.Errorf("tool %q is listed more than once for %s", id, option)
		}
	}
	*ids = append(*ids, id)
	return nil
}

func runToolManagement(root string, stdout, stderr io.Writer, opts options, manager toolManager) int {
	for _, id := range opts.revoke {
		removed, err := manager.Revoke(root, id)
		if err != nil {
			writeError(stderr, "tool/revoke", "cannot revoke tool approval", err.Error())
			return 2
		}
		if _, err := fmt.Fprintf(stdout, "Revoked %d approval(s) for tool %s.\n", removed, render.SafeText(string(id))); err != nil {
			writeError(stderr, "output/write", "cannot write tool approval output", err.Error())
			return 2
		}
	}
	for _, id := range opts.approve {
		target, err := manager.Approve(root, id)
		if err != nil {
			if unavailable, ok := safeexec.AsUnavailable(err); ok {
				writeDiagnostic(stderr, diagnostic.Diagnostic{Level: diagnostic.Error, Code: unavailable.Code, Summary: unavailable.Summary, Evidence: unavailable.Evidence, Action: unavailable.Action})
			} else {
				writeError(stderr, "tool/approve", "cannot approve tool", err.Error())
			}
			return 2
		}
		if err := writeApprovedTool(stdout, target); err != nil {
			writeError(stderr, "output/write", "cannot write tool approval output", err.Error())
			return 2
		}
	}
	if opts.tools {
		if err := writeToolStatuses(stdout, manager.Statuses(root)); err != nil {
			writeError(stderr, "output/write", "cannot write tool status output", err.Error())
			return 2
		}
	}
	return 0
}

func writeApprovedTool(w io.Writer, target safeexec.Target) error {
	scopeText := "all repositories"
	if target.Repository != "" {
		scopeText = target.Repository
	}
	_, err := fmt.Fprintf(w, "Approved tool %s.\n  executable: %s\n  sha256: %s\n  scope: %s\n", render.SafeText(string(target.Tool)), render.SafeText(target.Path), target.SHA256, render.SafeText(scopeText))
	return err
}

func writeToolStatuses(w io.Writer, statuses []safeexec.Status) error {
	for _, status := range statuses {
		if _, err := fmt.Fprintln(w, render.SafeText(string(status.Tool))); err != nil {
			return err
		}
		switch status.State {
		case safeexec.UnavailableState:
			if _, err := fmt.Fprintln(w, "  status: UNAVAILABLE"); err != nil {
				return err
			}
			if status.Problem != nil {
				if _, err := fmt.Fprintf(w, "  code: %s\n  summary: %s\n", render.SafeText(status.Problem.Code), render.SafeText(status.Problem.Summary)); err != nil {
					return err
				}
				if status.Problem.Evidence != "" {
					if _, err := fmt.Fprintf(w, "  evidence: %s\n", render.SafeText(status.Problem.Evidence)); err != nil {
						return err
					}
				}
				if status.Problem.Action != "" {
					if _, err := fmt.Fprintf(w, "  action: %s\n", render.SafeText(status.Problem.Action)); err != nil {
						return err
					}
				}
			}
		case safeexec.ApprovedState:
			if _, err := fmt.Fprintln(w, "  status: APPROVED"); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintln(w, "  status: NOT APPROVED"); err != nil {
				return err
			}
		}
		if status.Target.Path != "" {
			if _, err := fmt.Fprintf(w, "  executable: %s\n  origin: %s\n", render.SafeText(status.Target.Path), status.Target.Origin); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeError(w io.Writer, code, summary, evidence string) {
	writeDiagnostic(w, diagnostic.Diagnostic{
		Level:    diagnostic.Error,
		Code:     code,
		Summary:  summary,
		Evidence: evidence,
	})
}

func writeDiagnostic(w io.Writer, message diagnostic.Diagnostic) {
	_, _ = fmt.Fprintf(w, "awareof: %s [%s] %s\n", message.Level, render.SafeText(message.Code), render.SafeText(message.Summary))
	if message.Evidence != "" {
		_, _ = fmt.Fprintf(w, "  evidence: %s\n", render.SafeText(message.Evidence))
	}
	if message.Action != "" {
		_, _ = fmt.Fprintf(w, "  action: %s\n", render.SafeText(message.Action))
	}
}
