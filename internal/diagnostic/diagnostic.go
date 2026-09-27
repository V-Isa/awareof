// Package diagnostic defines core-owned operational messages.
package diagnostic

import "github.com/V-Isa/awareof/internal/vocabulary"

// Level identifies the importance of an operational message. Diagnostics are
// separate from provider states and contract statuses.
type Level string

const (
	Error   Level = "ERROR"
	Warning Level = "WARNING"
	Info    Level = "INFO"
)

// Diagnostic is one structured operational message.
type Diagnostic struct {
	Level    Level  `json:"level"`
	Code     string `json:"code"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence,omitempty"`
	Action   string `json:"action,omitempty"`
}

// Valid reports whether the diagnostic uses the normative core vocabulary.
func (d Diagnostic) Valid() bool {
	if d.Level != Error && d.Level != Warning && d.Level != Info {
		return false
	}
	if !vocabulary.ValidCode(d.Code) || d.Summary == "" {
		return false
	}
	return true
}
