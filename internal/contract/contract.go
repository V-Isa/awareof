// Package contract loads and evaluates sparse repository scope assertions.
package contract

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/V-Isa/awareof/internal/pathset"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	Version               = 1
	maxContractSize       = 4 * 1024 * 1024
	maxContractRules      = 10_000
	primaryContractFile   = ".awareof.yaml"
	alternateContractFile = ".awareof.yml"
)

// Rule is one path pattern and its partial provider assertions.
type Rule struct {
	Pattern    string
	Assertions map[scope.ProviderID]scope.State
}

// Contract is the parsed repository contract.
type Contract struct {
	File  string
	Rules []Rule
}

// Status describes one evaluated assertion. It is separate from provider
// states and never becomes a fifth provider state.
type Status string

const (
	Satisfied  Status = "SATISFIED"
	Mismatch   Status = "MISMATCH"
	Unresolved Status = "UNRESOLVED"
)

// Valid reports whether status belongs to the normalized contract model.
func (s Status) Valid() bool {
	switch s {
	case Satisfied, Mismatch, Unresolved:
		return true
	default:
		return false
	}
}

// Check is one resolved assertion for one concrete path and provider.
type Check struct {
	Path     scope.Path       `json:"path"`
	Pattern  string           `json:"pattern"`
	Provider scope.ProviderID `json:"provider"`
	Expected scope.State      `json:"expected"`
	Status   Status           `json:"status"`
	Actual   []scope.Result   `json:"actual"`
}

// Report is the deterministic result of validating a contract.
type Report struct {
	Contract string  `json:"contract"`
	Checks   []Check `json:"checks"`
}

// Valid reports whether every evaluated assertion is satisfied.
func (r Report) Valid() bool {
	for _, check := range r.Checks {
		if check.Status != Satisfied {
			return false
		}
	}
	return true
}

// Load reads the single contract at the repository root and validates its
// complete, deliberately small schema.
func Load(rootPath string, knownProviders []scope.ProviderID) (_ Contract, returnErr error) {
	if rootPath == "" {
		return Contract{}, errors.New("repository root is empty")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return Contract{}, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()

	file, err := findContract(root)
	if err != nil {
		return Contract{}, err
	}
	content, err := readContract(root, file)
	if err != nil {
		return Contract{}, err
	}
	known := make(map[scope.ProviderID]struct{}, len(knownProviders))
	for _, providerID := range knownProviders {
		if providerID != "" {
			known[providerID] = struct{}{}
		}
	}
	parsed, err := parse(file, content, known)
	if err != nil {
		return Contract{}, fmt.Errorf("parse %s: %w", file, err)
	}
	return parsed, nil
}

func findContract(root *os.Root) (string, error) {
	primary, err := regularFileExists(root, primaryContractFile)
	if err != nil {
		return "", err
	}
	alternate, err := regularFileExists(root, alternateContractFile)
	if err != nil {
		return "", err
	}
	if primary && alternate {
		return "", fmt.Errorf("both %s and %s exist; keep only one", primaryContractFile, alternateContractFile)
	}
	if primary {
		return primaryContractFile, nil
	}
	if alternate {
		return alternateContractFile, nil
	}
	return "", fmt.Errorf("no contract found; create %s at the repository root", primaryContractFile)
}

func regularFileExists(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.Mode().IsRegular():
		return true, nil
	case err == nil:
		return false, fmt.Errorf("%s must be a regular file, not a symlink or directory", name)
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("inspect %s: %w", name, err)
	}
}

func readContract(root *os.Root, name string) (_ []byte, returnErr error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close %s: %w", name, err))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, maxContractSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(content) > maxContractSize {
		return nil, fmt.Errorf("read %s: file exceeds %d bytes", name, maxContractSize)
	}
	return content, nil
}

func parse(file string, content []byte, known map[scope.ProviderID]struct{}) (Contract, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&document); err != nil {
		return Contract{}, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Contract{}, errors.New("multiple YAML documents are not supported")
		}
		return Contract{}, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Contract{}, errors.New("document must be a mapping with version and rules")
	}
	if err := rejectAdvancedYAML(document.Content[0]); err != nil {
		return Contract{}, err
	}

	root := document.Content[0]
	fields, err := mapping(root, "document")
	if err != nil {
		return Contract{}, err
	}
	for key := range fields {
		if key != "version" && key != "rules" {
			return Contract{}, fmt.Errorf("unknown document field %q", key)
		}
	}
	versionNode, ok := fields["version"]
	if !ok {
		return Contract{}, errors.New("missing required field \"version\"")
	}
	if versionNode.Kind != yaml.ScalarNode || versionNode.Tag != "!!int" || versionNode.Value != "1" {
		return Contract{}, fmt.Errorf("version must be integer %d", Version)
	}
	rulesNode, ok := fields["rules"]
	if !ok {
		return Contract{}, errors.New("missing required field \"rules\"")
	}
	ruleNodes, err := mapping(rulesNode, "rules")
	if err != nil {
		return Contract{}, err
	}
	if len(ruleNodes) > maxContractRules {
		return Contract{}, fmt.Errorf("rules contains %d entries, maximum is %d", len(ruleNodes), maxContractRules)
	}

	rules := make([]Rule, 0, len(ruleNodes))
	normalizedPatterns := make(map[string]string, len(ruleNodes))
	for rawPattern, assertionsNode := range ruleNodes {
		pattern, err := pathset.NormalizeRootPattern(rawPattern)
		if err != nil {
			return Contract{}, fmt.Errorf("invalid rule pattern %q: %w", rawPattern, err)
		}
		if previous, exists := normalizedPatterns[pattern]; exists {
			return Contract{}, fmt.Errorf("rule patterns %q and %q normalize to the same pattern %q", previous, rawPattern, pattern)
		}
		normalizedPatterns[pattern] = rawPattern
		assertionNodes, err := mapping(assertionsNode, fmt.Sprintf("rule %q", rawPattern))
		if err != nil {
			return Contract{}, err
		}
		if len(assertionNodes) == 0 {
			return Contract{}, fmt.Errorf("rule %q has no provider assertions", rawPattern)
		}
		assertions := make(map[scope.ProviderID]scope.State, len(assertionNodes))
		for rawProvider, stateNode := range assertionNodes {
			providerID := scope.ProviderID(rawProvider)
			if _, exists := known[providerID]; !exists {
				return Contract{}, fmt.Errorf("rule %q references unknown provider %q", rawPattern, rawProvider)
			}
			if stateNode.Kind != yaml.ScalarNode || stateNode.Tag != "!!str" {
				return Contract{}, fmt.Errorf("rule %q provider %q state must be \"in\" or \"out\"", rawPattern, rawProvider)
			}
			var expected scope.State
			switch stateNode.Value {
			case "in":
				expected = scope.In
			case "out":
				expected = scope.Out
			default:
				return Contract{}, fmt.Errorf("rule %q provider %q state must be \"in\" or \"out\", got %q", rawPattern, rawProvider, stateNode.Value)
			}
			assertions[providerID] = expected
		}
		rules = append(rules, Rule{Pattern: pattern, Assertions: assertions})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Pattern < rules[j].Pattern })
	return Contract{File: file, Rules: rules}, nil
}

func rejectAdvancedYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || (node.Value == "<<" && node.Tag == "!!merge") {
		return errors.New("YAML aliases, anchors, and merge keys are not supported")
	}
	if node.Tag != "" && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("custom YAML tag %q is not supported", node.Tag)
	}
	for _, child := range node.Content {
		if err := rejectAdvancedYAML(child); err != nil {
			return err
		}
	}
	return nil
}

func mapping(node *yaml.Node, location string) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", location)
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("%s keys must be strings", location)
		}
		if _, exists := result[key.Value]; exists {
			return nil, fmt.Errorf("%s contains duplicate key %q", location, key.Value)
		}
		result[key.Value] = node.Content[index+1]
	}
	return result, nil
}

// MatchingPaths keeps only paths covered by at least one contract rule.
func (c Contract) MatchingPaths(paths []scope.Path) ([]scope.Path, error) {
	matched := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		for _, rule := range c.Rules {
			ok, err := pathset.MatchPattern(rule.Pattern, name)
			if err != nil {
				return nil, fmt.Errorf("match contract pattern %q: %w", rule.Pattern, err)
			}
			if ok {
				matched = append(matched, name)
				break
			}
		}
	}
	return matched, nil
}

// Evaluate compares effective provider facts with all applicable assertions.
func (c Contract) Evaluate(paths []scope.Path, results []scope.Result) (Report, error) {
	byPathProvider := make(map[scope.Path]map[scope.ProviderID][]scope.Result)
	for _, result := range results {
		if byPathProvider[result.Path] == nil {
			byPathProvider[result.Path] = make(map[scope.ProviderID][]scope.Result)
		}
		byPathProvider[result.Path][result.Provider] = append(byPathProvider[result.Path][result.Provider], result)
	}

	report := Report{Contract: c.File, Checks: []Check{}}
	for _, name := range paths {
		assertions, err := c.assertionsFor(name)
		if err != nil {
			return Report{}, err
		}
		providerIDs := make([]scope.ProviderID, 0, len(assertions))
		for providerID := range assertions {
			providerIDs = append(providerIDs, providerID)
		}
		sort.Slice(providerIDs, func(i, j int) bool { return providerIDs[i] < providerIDs[j] })
		for _, providerID := range providerIDs {
			assertion := assertions[providerID]
			actual := append([]scope.Result{}, byPathProvider[name][providerID]...)
			sort.Slice(actual, func(i, j int) bool { return actual[i].Instance < actual[j].Instance })
			report.Checks = append(report.Checks, Check{
				Path:     name,
				Pattern:  assertion.pattern,
				Provider: providerID,
				Expected: assertion.state,
				Status:   classify(actual, assertion.state),
				Actual:   actual,
			})
		}
	}
	return report, nil
}

// RequiredProviders returns the providers asserted for the selected paths. It
// also resolves specificity before any provider executable may be invoked.
func (c Contract) RequiredProviders(paths []scope.Path) ([]scope.ProviderID, error) {
	set := make(map[scope.ProviderID]struct{})
	for _, name := range paths {
		assertions, err := c.assertionsFor(name)
		if err != nil {
			return nil, err
		}
		for providerID := range assertions {
			set[providerID] = struct{}{}
		}
	}
	ids := make([]scope.ProviderID, 0, len(set))
	for providerID := range set {
		ids = append(ids, providerID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

type resolvedAssertion struct {
	pattern string
	state   scope.State
}

func (c Contract) assertionsFor(name scope.Path) (map[scope.ProviderID]resolvedAssertion, error) {
	candidates := make(map[scope.ProviderID][]resolvedAssertion)
	for _, rule := range c.Rules {
		matched, err := pathset.MatchPattern(rule.Pattern, name)
		if err != nil {
			return nil, fmt.Errorf("match contract pattern %q: %w", rule.Pattern, err)
		}
		if !matched {
			continue
		}
		for providerID, state := range rule.Assertions {
			candidates[providerID] = append(candidates[providerID], resolvedAssertion{pattern: rule.Pattern, state: state})
		}
	}

	resolved := make(map[scope.ProviderID]resolvedAssertion, len(candidates))
	for providerID, providerAssertions := range candidates {
		maximal := maximalAssertions(providerAssertions)
		state := maximal[0].state
		for _, assertion := range maximal[1:] {
			if assertion.state != state {
				patterns := make([]string, 0, len(maximal))
				for _, item := range maximal {
					patterns = append(patterns, fmt.Sprintf("%q=%s", item.pattern, strings.ToLower(string(item.state))))
				}
				sort.Strings(patterns)
				return nil, fmt.Errorf("path %q has ambiguous %s assertions: %s", name, providerID, strings.Join(patterns, ", "))
			}
		}
		sort.Slice(maximal, func(i, j int) bool { return maximal[i].pattern < maximal[j].pattern })
		resolved[providerID] = maximal[0]
	}
	return resolved, nil
}

func maximalAssertions(assertions []resolvedAssertion) []resolvedAssertion {
	maximal := make([]resolvedAssertion, 0, len(assertions))
	for index, assertion := range assertions {
		dominated := false
		for otherIndex, other := range assertions {
			if index != otherIndex && moreSpecific(other.pattern, assertion.pattern) {
				dominated = true
				break
			}
		}
		if !dominated {
			maximal = append(maximal, assertion)
		}
	}
	return maximal
}

func moreSpecific(narrow, broad string) bool {
	if narrow == broad {
		return false
	}
	if !hasMeta(narrow) {
		return true
	}
	if broad == "**" {
		return true
	}
	prefix, ok := strings.CutSuffix(broad, "/**")
	if !ok || hasMeta(prefix) {
		return false
	}
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(narrow, prefix+"/")
}

func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func classify(results []scope.Result, expected scope.State) Status {
	relevant := 0
	unresolved := false
	for _, result := range results {
		switch result.State {
		case scope.NotApp:
			continue
		case scope.Unknown:
			relevant++
			unresolved = true
		case expected:
			relevant++
		case scope.In, scope.Out:
			// A known contradiction takes precedence over any UNKNOWN result.
			return Mismatch
		}
	}
	if relevant == 0 {
		return Unresolved
	}
	if unresolved {
		return Unresolved
	}
	return Satisfied
}

// Patterns returns the contract patterns in deterministic order.
func (c Contract) Patterns() []string {
	patterns := make([]string, len(c.Rules))
	for index, rule := range c.Rules {
		patterns[index] = rule.Pattern
	}
	return patterns
}
