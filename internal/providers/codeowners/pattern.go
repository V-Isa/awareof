package codeowners

import (
	"errors"
	"strings"
	"unicode"
)

type pathPattern struct {
	segments         []segmentPattern
	matchDescendants bool
}

// compilePattern implements the documented GitHub CODEOWNERS pattern subset.
// In particular, matching is case-sensitive, * and ? do not cross '/', ** is
// recursive, a leading or non-trailing slash anchors a pattern at the
// repository root, and [] is literal rather than a character range.
func compilePattern(pattern string) (pathPattern, error) {
	switch {
	case pattern == "":
		return pathPattern{}, errors.New("pattern is empty")
	case pattern == "/":
		return pathPattern{segments: []segmentPattern{{never: true}}}, nil
	case strings.Contains(pattern, "***"):
		return pathPattern{}, errors.New("pattern contains three consecutive asterisks")
	case strings.HasPrefix(pattern, "!"):
		return pathPattern{}, errors.New("negation is not supported")
	}
	for _, current := range pattern {
		if current == 0 {
			return pathPattern{}, errors.New("pattern contains NUL")
		}
		if unicode.IsControl(current) || (unicode.IsSpace(current) && current != ' ') {
			return pathPattern{}, errors.New("pattern contains unescaped whitespace or a control character")
		}
	}

	rawSegments := strings.Split(pattern, "/")
	if rawSegments[0] == "" {
		rawSegments = rawSegments[1:]
	} else if len(rawSegments) == 1 || (len(rawSegments) == 2 && rawSegments[1] == "") {
		if rawSegments[0] != "**" {
			rawSegments = append([]string{"**"}, rawSegments...)
		}
	}
	for index, segment := range rawSegments {
		if segment == "" && index != len(rawSegments)-1 {
			return pathPattern{}, errors.New("pattern contains an empty path segment")
		}
	}
	if len(rawSegments) > 1 && rawSegments[len(rawSegments)-1] == "" {
		rawSegments[len(rawSegments)-1] = "**"
	}

	segments := make([]segmentPattern, 0, len(rawSegments))
	for _, raw := range rawSegments {
		segment, err := compileSegment(raw)
		if err != nil {
			return pathPattern{}, err
		}
		segments = append(segments, segment)
	}
	last := rawSegments[len(rawSegments)-1]
	return pathPattern{
		segments:         segments,
		matchDescendants: last != "*" && last != "**",
	}, nil
}

// MatchString reports whether name is matched as a repository-relative path.
func (p pathPattern) MatchString(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	pathSegments := strings.Split(name, "/")
	reachable := make([]bool, len(pathSegments)+1)
	reachable[0] = true
	for index, segment := range p.segments {
		next := make([]bool, len(reachable))
		if segment.recursive {
			seen := false
			for consumed := range reachable {
				if index == len(p.segments)-1 && consumed > 0 && seen {
					next[consumed] = true
				}
				if reachable[consumed] {
					seen = true
				}
				if index != len(p.segments)-1 && seen {
					next[consumed] = true
				}
			}
		} else {
			for consumed := 0; consumed < len(pathSegments); consumed++ {
				if reachable[consumed] && segment.matches(pathSegments[consumed]) {
					next[consumed+1] = true
				}
			}
		}
		reachable = next
		if !anyReachable(reachable) {
			return false
		}
	}
	if p.matchDescendants {
		return anyReachable(reachable)
	}
	return reachable[len(pathSegments)]
}

func anyReachable(positions []bool) bool {
	for _, reachable := range positions {
		if reachable {
			return true
		}
	}
	return false
}

type segmentTokenKind uint8

const (
	literalToken segmentTokenKind = iota
	starToken
	questionToken
)

type segmentToken struct {
	kind  segmentTokenKind
	value rune
}

type segmentPattern struct {
	recursive bool
	never     bool
	tokens    []segmentToken
}

func compileSegment(segment string) (segmentPattern, error) {
	if segment == "**" {
		return segmentPattern{recursive: true}, nil
	}
	escaped := false
	var tokens []segmentToken
	for _, current := range segment {
		if escaped {
			tokens = append(tokens, segmentToken{kind: literalToken, value: current})
			escaped = false
			continue
		}
		switch current {
		case '\\':
			escaped = true
		case '*':
			tokens = append(tokens, segmentToken{kind: starToken})
		case '?':
			tokens = append(tokens, segmentToken{kind: questionToken})
		default:
			tokens = append(tokens, segmentToken{kind: literalToken, value: current})
		}
	}
	if escaped {
		return segmentPattern{}, errors.New("pattern ends with an incomplete escape")
	}
	return segmentPattern{tokens: tokens}, nil
}

func (p segmentPattern) matches(name string) bool {
	if p.never {
		return false
	}
	runes := []rune(name)
	tokenIndex, runeIndex := 0, 0
	starIndex, starRune := -1, 0
	for runeIndex < len(runes) {
		switch {
		case tokenIndex < len(p.tokens) && p.tokens[tokenIndex].kind == questionToken:
			tokenIndex++
			runeIndex++
		case tokenIndex < len(p.tokens) && p.tokens[tokenIndex].kind == literalToken && p.tokens[tokenIndex].value == runes[runeIndex]:
			tokenIndex++
			runeIndex++
		case tokenIndex < len(p.tokens) && p.tokens[tokenIndex].kind == starToken:
			starIndex = tokenIndex
			starRune = runeIndex
			tokenIndex++
		case starIndex >= 0:
			starRune++
			runeIndex = starRune
			tokenIndex = starIndex + 1
		default:
			return false
		}
	}
	for tokenIndex < len(p.tokens) && p.tokens[tokenIndex].kind == starToken {
		tokenIndex++
	}
	return tokenIndex == len(p.tokens)
}
