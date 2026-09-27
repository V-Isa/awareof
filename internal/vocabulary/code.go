// Package vocabulary defines shared validation for stable machine vocabulary.
package vocabulary

import "strings"

// ValidCode reports whether a value is a lowercase slash-separated code. Each
// segment may contain ASCII letters, digits, and internal hyphens.
func ValidCode(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !ValidIdentifier(part) {
			return false
		}
	}
	return true
}

// ValidIdentifier reports whether a value is a lowercase ASCII identifier.
// Identifiers may contain digits and internal hyphens.
func ValidIdentifier(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' || !lowerAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if !lowerAlphaNumeric(character) && character != '-' {
			return false
		}
	}
	return true
}

func lowerAlphaNumeric(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9')
}
