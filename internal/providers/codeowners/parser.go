package codeowners

import (
	"fmt"
	"strings"
	"unicode"
)

type rule struct {
	rawPattern string
	owners     []string
	line       int
	matcher    pathPattern
}

func (r rule) matches(name string) bool {
	return r.matcher.MatchString(name)
}

type parseIssue struct {
	line   int
	reason string
}

func parseRules(content string) ([]rule, []parseIssue) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	rules := make([]rule, 0, len(lines))
	var issues []parseIssue
	for index, source := range lines {
		parsed, present, err := parseLine(strings.TrimSuffix(source, "\r"), index+1)
		if err != nil {
			issues = append(issues, parseIssue{line: index + 1, reason: err.Error()})
			continue
		}
		if present {
			rules = append(rules, parsed)
		}
	}
	return rules, issues
}

func parseLine(source string, line int) (rule, bool, error) {
	trimmed := strings.TrimLeft(source, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, `\#`) {
		return rule{}, false, nil
	}

	patternEnd := patternBoundary(trimmed)
	pattern := trimmed[:patternEnd]
	if pattern == "" {
		return rule{}, false, nil
	}
	matcher, err := compilePattern(pattern)
	if err != nil {
		return rule{}, false, fmt.Errorf("invalid pattern: %w", err)
	}

	remainder := strings.TrimSpace(trimmed[patternEnd:])
	if comment := strings.IndexByte(remainder, '#'); comment >= 0 {
		remainder = remainder[:comment]
	}
	owners := strings.Fields(remainder)
	for _, owner := range owners {
		if !validOwner(owner) {
			return rule{}, false, fmt.Errorf("invalid owner %q", owner)
		}
	}
	return rule{rawPattern: pattern, owners: owners, line: line, matcher: matcher}, true, nil
}

func patternBoundary(line string) int {
	escaped := false
	for index, current := range line {
		if (current == ' ' || current == '\t') && !escaped {
			return index
		}
		if current == '\\' && !escaped {
			escaped = true
			continue
		}
		escaped = false
	}
	return len(line)
}

func validOwner(owner string) bool {
	for _, current := range owner {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	if strings.HasPrefix(owner, "@") {
		identifier := strings.TrimPrefix(owner, "@")
		parts := strings.Split(identifier, "/")
		if identifier == "" || len(parts) > 2 {
			return false
		}
		for _, part := range parts {
			if part == "" || !validAccountPart(part) {
				return false
			}
		}
		return true
	}
	if strings.Count(owner, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(owner, "@")
	return local != "" && domain != "" && !strings.ContainsAny(owner, "/\\")
}

func validAccountPart(part string) bool {
	for _, current := range part {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || current == '-' || current == '_' {
			continue
		}
		return false
	}
	return true
}
