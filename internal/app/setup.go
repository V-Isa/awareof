package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/V-Isa/awareof/internal/diagnostic"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/providers/git"
	"github.com/V-Isa/awareof/internal/providers/npm"
	"github.com/V-Isa/awareof/internal/providers/typescript"
	"github.com/V-Isa/awareof/internal/render"
	"github.com/V-Isa/awareof/internal/safeexec"
)

const maxSetupAnswerBytes = 1024

type nativeProviders struct {
	git        *git.Provider
	npm        *npm.Provider
	typescript *typescript.Provider
}

func terminalInteraction(input io.Reader, display, prompt io.Writer) bool {
	return terminalFile(input) && terminalFile(display) && terminalFile(prompt)
}

func terminalFile(value any) bool {
	file, ok := value.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func runSetup(
	root string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	manager toolManager,
	statuses []safeexec.Status,
	interactive bool,
) int {
	statuses = append([]safeexec.Status(nil), statuses...)
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Tool != statuses[j].Tool {
			return statuses[i].Tool < statuses[j].Tool
		}
		return statuses[i].Target.Path < statuses[j].Target.Path
	})
	if err := writeSetupStatuses(stdout, statuses); err != nil {
		writeError(stderr, "output/write", "cannot write setup output", err.Error())
		return 2
	}

	external, repository := pendingSetupTargets(statuses)
	pending := len(external) + len(repository)
	unavailable := unavailableToolCount(statuses)
	if pending == 0 {
		message := "Setup complete: no approvals required."
		if unavailable != 0 {
			message = fmt.Sprintf("Setup finished: %s unavailable; no approvals changed.", toolCount(unavailable, "native tool", "native tools"))
		}
		if _, err := fmt.Fprintln(stdout, message); err != nil {
			writeError(stderr, "output/write", "cannot write setup output", err.Error())
			return 2
		}
		return 0
	}
	if !interactive {
		if _, err := fmt.Fprintln(stdout, "Setup is noninteractive; no approvals changed.\nUse --approve-tool for explicit noninteractive approval, or run --setup in a terminal."); err != nil {
			writeError(stderr, "output/write", "cannot write setup output", err.Error())
			return 2
		}
		return 0
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 128), maxSetupAnswerBytes)
	approved := 0
	if len(external) != 0 {
		question := fmt.Sprintf("Approve %s shown above? [y/N] ", toolCount(len(external), "external tool", "external tools"))
		confirmed, err := setupConfirmation(stderr, scanner, question)
		if err != nil {
			writeError(stderr, "setup/input", "cannot read setup confirmation", err.Error())
			return 2
		}
		if confirmed {
			count, code := approveSetupTargets(root, stdout, stderr, manager, external)
			if code != 0 {
				return code
			}
			approved += count
		}
	}

	if len(repository) != 0 {
		if _, err := fmt.Fprintln(stdout, "Repository-controlled tools require separate approval for this repository."); err != nil {
			writeError(stderr, "output/write", "cannot write setup output", err.Error())
			return 2
		}
		for _, status := range repository {
			question := fmt.Sprintf(
				"Approve repository-controlled tool %s at %s? [y/N] ",
				render.SafeText(string(status.Tool)),
				render.SafeText(status.Target.Path),
			)
			confirmed, err := setupConfirmation(stderr, scanner, question)
			if err != nil {
				writeError(stderr, "setup/input", "cannot read setup confirmation", err.Error())
				return 2
			}
			if !confirmed {
				continue
			}
			count, code := approveSetupTargets(root, stdout, stderr, manager, []safeexec.Status{status})
			if code != 0 {
				return code
			}
			approved += count
		}
	}

	message := setupCompletion(approved, unavailable)
	if _, err := fmt.Fprintln(stdout, message); err != nil {
		writeError(stderr, "output/write", "cannot write setup output", err.Error())
		return 2
	}
	return 0
}

func relevantNativeTools(
	ctx context.Context,
	root string,
	providers nativeProviders,
) ([]safeexec.Selection, error) {
	repo := provider.Repository{Root: root}
	relevant := make(map[safeexec.Selection]struct{})

	gitInstances, err := providers.git.Detect(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("detect Git provider: %w", err)
	}
	if len(gitInstances) != 0 {
		relevant[safeexec.Selection{Tool: "git"}] = struct{}{}
	}

	npmInstances, err := providers.npm.Detect(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("detect npm provider: %w", err)
	}
	if providers.npm.NeedsNativeTools(npmInstances) {
		relevant[safeexec.Selection{Tool: "node"}] = struct{}{}
		relevant[safeexec.Selection{Tool: "npm"}] = struct{}{}
	}

	if providers.typescript != nil {
		typescriptInstances, err := providers.typescript.Detect(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("detect TypeScript provider: %w", err)
		}
		selections, err := providers.typescript.SetupSelections(root, typescriptInstances)
		if err != nil {
			return nil, fmt.Errorf("discover TypeScript native tools: %w", err)
		}
		for _, selection := range selections {
			relevant[selection] = struct{}{}
		}
	}

	selections := make([]safeexec.Selection, 0, len(relevant))
	for selection := range relevant {
		selections = append(selections, selection)
	}
	sort.Slice(selections, func(i, j int) bool {
		if selections[i].Tool != selections[j].Tool {
			return selections[i].Tool < selections[j].Tool
		}
		return selections[i].Path < selections[j].Path
	})
	return selections, nil
}

func writeSetupStatuses(w io.Writer, statuses []safeexec.Status) error {
	if _, err := fmt.Fprintln(w, "Native tools"); err != nil {
		return err
	}
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(w, "\n  None required for detected providers.")
		return err
	}
	for _, status := range statuses {
		if _, err := fmt.Fprintf(w, "\n%s\n", render.SafeText(string(status.Tool))); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  status: %s\n", setupStatusText(status.State)); err != nil {
			return err
		}
		if status.Target.Path != "" {
			if _, err := fmt.Fprintf(
				w,
				"  entry point: %s\n  sha256: %s\n  origin: %s\n",
				render.SafeText(status.Target.Path),
				status.Target.SHA256,
				status.Target.Origin,
			); err != nil {
				return err
			}
			if status.Target.Repository != "" {
				if _, err := fmt.Fprintf(w, "  repository: %s\n", render.SafeText(status.Target.Repository)); err != nil {
					return err
				}
			}
		}
		if status.Problem != nil {
			if status.Problem.Code != "" {
				if _, err := fmt.Fprintf(w, "  code: %s\n", render.SafeText(status.Problem.Code)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "  summary: %s\n", render.SafeText(status.Problem.Summary)); err != nil {
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
	}
	_, err := fmt.Fprintln(w)
	return err
}

func setupStatusText(state safeexec.ApprovalState) string {
	if state == safeexec.NotApprovedState {
		return "NOT APPROVED"
	}
	return string(state)
}

func pendingSetupTargets(statuses []safeexec.Status) (external, repository []safeexec.Status) {
	for _, status := range statuses {
		if status.State != safeexec.NotApprovedState || status.Target.Path == "" {
			continue
		}
		switch status.Target.Origin {
		case safeexec.ExternalOrigin:
			external = append(external, status)
		case safeexec.RepositoryOrigin:
			repository = append(repository, status)
		}
	}
	return external, repository
}

func unavailableToolCount(statuses []safeexec.Status) int {
	count := 0
	for _, status := range statuses {
		if status.State == safeexec.UnavailableState {
			count++
		}
	}
	return count
}

func setupConfirmation(w io.Writer, scanner *bufio.Scanner, question string) (bool, error) {
	if _, err := io.WriteString(w, question); err != nil {
		return false, fmt.Errorf("write confirmation: %w", err)
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("read confirmation: %w", err)
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

func approveSetupTargets(
	root string,
	stdout io.Writer,
	stderr io.Writer,
	manager toolManager,
	statuses []safeexec.Status,
) (int, int) {
	approved := 0
	for _, status := range statuses {
		target, err := manager.ApproveTarget(root, status.Target)
		if err != nil {
			if unavailable, ok := safeexec.AsUnavailable(err); ok {
				writeDiagnostic(stderr, diagnostic.Diagnostic{
					Level:    diagnostic.Error,
					Code:     unavailable.Code,
					Summary:  unavailable.Summary,
					Evidence: unavailable.Evidence,
					Action:   unavailable.Action,
				})
			} else {
				writeError(stderr, "tool/approve", "cannot approve tool", err.Error())
			}
			return approved, 2
		}
		if _, err := fmt.Fprintf(stdout, "Approved tool %s.\n", render.SafeText(string(target.Tool))); err != nil {
			writeError(stderr, "output/write", "cannot write setup output", err.Error())
			return approved, 2
		}
		approved++
	}
	return approved, 0
}

func toolCount(count int, singular, plural string) string {
	word := plural
	if count == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", count, word)
}

func setupCompletion(approved, unavailable int) string {
	if unavailable != 0 {
		if approved == 0 {
			return fmt.Sprintf("Setup finished: %s unavailable; no approvals changed.", toolCount(unavailable, "native tool", "native tools"))
		}
		return fmt.Sprintf(
			"Setup finished: approved %s; %s unavailable.",
			toolCount(approved, "tool", "tools"),
			toolCount(unavailable, "native tool", "native tools"),
		)
	}
	if approved == 0 {
		return "Setup complete: no approvals changed."
	}
	return fmt.Sprintf("Setup complete: approved %s.", toolCount(approved, "tool", "tools"))
}
