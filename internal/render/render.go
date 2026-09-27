package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/contract"
	"github.com/V-Isa/awareof/internal/scope"
)

const schemaVersion = 1

// Document is the current machine-readable inspection envelope. It remains
// internal and may evolve before the first public release.
type Document struct {
	SchemaVersion int            `json:"schemaVersion"`
	Paths         []scope.Path   `json:"paths"`
	Results       []scope.Result `json:"results"`
}

// ValidationDocument is the machine-readable validation envelope.
type ValidationDocument struct {
	SchemaVersion int              `json:"schemaVersion"`
	Valid         bool             `json:"valid"`
	Contract      string           `json:"contract"`
	Checks        []contract.Check `json:"checks"`
}

// JSON writes the complete machine-readable inspection document.
func JSON(w io.Writer, paths []scope.Path, results []scope.Result) error {
	document := Document{
		SchemaVersion: schemaVersion,
		Paths:         nonNil(paths),
		Results:       nonNil(results),
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}

// Human writes one provider row per path and provider instance.
func Human(w io.Writer, paths []scope.Path, results []scope.Result) error {
	if len(paths) == 0 {
		if _, err := fmt.Fprintln(w, "No paths matched."); err != nil {
			return fmt.Errorf("write human output: %w", err)
		}
		return nil
	}
	if len(results) == 0 {
		if _, err := fmt.Fprintln(w, "No providers detected."); err != nil {
			return fmt.Errorf("write human output: %w", err)
		}
		return nil
	}

	requested := make(map[scope.Path]struct{}, len(paths))
	for _, name := range paths {
		requested[name] = struct{}{}
	}
	byPath := make(map[scope.Path][]scope.Result, len(paths))
	for _, result := range results {
		if _, ok := requested[result.Path]; !ok {
			return fmt.Errorf("render human output: result path %q was not requested", result.Path)
		}
		byPath[result.Path] = append(byPath[result.Path], result)
	}
	setup := setupRequirements(results)
	for _, path := range paths {
		if _, err := fmt.Fprintln(w, SafeText(string(path))); err != nil {
			return fmt.Errorf("write human output: %w", err)
		}
		for _, result := range byPath[path] {
			name := string(result.Provider)
			if result.Instance != scope.InstanceID(result.Provider) {
				name += "/" + string(result.Instance)
			}
			if requiresToolSetup(result) {
				if _, err := fmt.Fprintf(w, "  %-16s %s\n", SafeText(name), result.State); err != nil {
					return fmt.Errorf("write human output: %w", err)
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "  %-16s %-7s %s\n", SafeText(name), result.State, SafeText(result.Explanation.Summary)); err != nil {
				return fmt.Errorf("write human output: %w", err)
			}
			if err := explanationDetails(w, "    ", result.Explanation); err != nil {
				return err
			}
		}
	}
	if err := writeToolSetupHint(w, setup); err != nil {
		return fmt.Errorf("write human output: %w", err)
	}
	return nil
}

type setupRequirement struct {
	tool        string
	explanation scope.Explanation
}

func setupRequirements(results []scope.Result) []setupRequirement {
	byTool := make(map[string]scope.Explanation)
	for _, result := range results {
		if !requiresToolSetup(result) {
			continue
		}
		if _, exists := byTool[result.Provenance.Tool]; !exists {
			byTool[result.Provenance.Tool] = result.Explanation
		}
	}
	tools := make([]string, 0, len(byTool))
	for tool := range byTool {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	requirements := make([]setupRequirement, 0, len(tools))
	for _, tool := range tools {
		requirements = append(requirements, setupRequirement{tool: tool, explanation: byTool[tool]})
	}
	return requirements
}

func requiresToolSetup(result scope.Result) bool {
	return result.State == scope.Unknown && result.Explanation.Code == "tool/not-approved" && result.Provenance.Tool != ""
}

func toolCount(count int) string {
	if count == 1 {
		return "1 native tool"
	}
	return fmt.Sprintf("%d native tools", count)
}

func writeToolSetupHint(w io.Writer, setup []setupRequirement) error {
	if len(setup) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\n%s requires setup.\n", toolCount(len(setup))); err != nil {
		return err
	}
	for _, requirement := range setup {
		if _, err := fmt.Fprintf(w, "  %s: %s\n", SafeText(requirement.tool), SafeText(requirement.explanation.Summary)); err != nil {
			return err
		}
		if requirement.explanation.Evidence != "" {
			if _, err := fmt.Fprintf(w, "    evidence: %s\n", SafeText(requirement.explanation.Evidence)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w, "Run awareof --setup (with the same --tool selections, if any).")
	return err
}

// ValidationJSON writes the complete validation report.
func ValidationJSON(w io.Writer, report contract.Report) error {
	document := ValidationDocument{
		SchemaVersion: schemaVersion,
		Valid:         report.Valid(),
		Contract:      report.Contract,
		Checks:        nonNil(report.Checks),
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode validation JSON output: %w", err)
	}
	return nil
}

// ValidationHuman writes failures with the provider evidence that caused them.
func ValidationHuman(w io.Writer, report contract.Report) error {
	if report.Valid() {
		if len(report.Checks) == 0 {
			if _, err := fmt.Fprintln(w, "Validation passed: no paths matched contract rules."); err != nil {
				return fmt.Errorf("write validation output: %w", err)
			}
			return nil
		}
		if _, err := fmt.Fprintf(w, "Validation passed: %d assertions satisfied.\n", len(report.Checks)); err != nil {
			return fmt.Errorf("write validation output: %w", err)
		}
		return nil
	}

	if _, err := fmt.Fprintln(w, "Validation failed."); err != nil {
		return fmt.Errorf("write validation output: %w", err)
	}
	var setupResults []scope.Result
	for _, check := range report.Checks {
		setupResults = append(setupResults, check.Actual...)
	}
	var previous scope.Path
	for _, check := range report.Checks {
		if check.Status == contract.Satisfied {
			continue
		}
		if check.Path != previous {
			if _, err := fmt.Fprintln(w, SafeText(string(check.Path))); err != nil {
				return fmt.Errorf("write validation output: %w", err)
			}
			previous = check.Path
		}
		if _, err := fmt.Fprintf(
			w,
			"  %s expected %s, actual %s (rule %s)\n",
			SafeText(string(check.Provider)),
			check.Expected,
			validationActual(check.Actual),
			SafeText(strconv.Quote(check.Pattern)),
		); err != nil {
			return fmt.Errorf("write validation output: %w", err)
		}
		if len(check.Actual) == 0 {
			if _, err := fmt.Fprintln(w, "    provider not detected or applicable"); err != nil {
				return fmt.Errorf("write validation output: %w", err)
			}
			continue
		}
		for _, actual := range check.Actual {
			name := string(actual.Provider)
			if actual.Instance != scope.InstanceID(actual.Provider) {
				name += "/" + string(actual.Instance)
			}
			if requiresToolSetup(actual) {
				if _, err := fmt.Fprintf(w, "    %s: %s\n", SafeText(name), actual.State); err != nil {
					return fmt.Errorf("write validation output: %w", err)
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "    %s: %s\n", SafeText(name), SafeText(actual.Explanation.Summary)); err != nil {
				return fmt.Errorf("write validation output: %w", err)
			}
			if err := explanationDetails(w, "      ", actual.Explanation); err != nil {
				return err
			}
		}
	}
	if err := writeToolSetupHint(w, setupRequirements(setupResults)); err != nil {
		return fmt.Errorf("write validation output: %w", err)
	}
	return nil
}

func explanationDetails(w io.Writer, indent string, explanation scope.Explanation) error {
	if explanation.Evidence != "" {
		if _, err := fmt.Fprintf(w, "%sevidence: %s\n", indent, SafeText(explanation.Evidence)); err != nil {
			return fmt.Errorf("write explanation evidence: %w", err)
		}
	}
	if explanation.Action != "" {
		if _, err := fmt.Fprintf(w, "%saction: %s\n", indent, SafeText(explanation.Action)); err != nil {
			return fmt.Errorf("write explanation action: %w", err)
		}
	}
	return nil
}

func validationActual(results []scope.Result) string {
	states := make(map[scope.State]struct{}, len(results))
	for _, result := range results {
		if result.State != scope.NotApp {
			states[result.State] = struct{}{}
		}
	}
	if len(states) == 0 {
		return string(scope.NotApp)
	}
	if len(states) > 1 {
		return "MIXED"
	}
	for state := range states {
		return string(state)
	}
	return string(scope.NotApp)
}

// SafeText quotes terminal controls, line breaks, and invisible formatting
// characters. Repository names and native diagnostics are untrusted.
func SafeText(value string) string {
	if !utf8.ValidString(value) {
		return strconv.QuoteToASCII(value)
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return strconv.QuoteToASCII(value)
		}
	}
	return value
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
