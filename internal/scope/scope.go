package scope

import "github.com/V-Isa/awareof/internal/vocabulary"

// Path is a normalized repository-root-relative logical path.
type Path string

// State is a provider's effective scope state for one path.
type State string

const (
	In      State = "IN"
	Out     State = "OUT"
	NotApp  State = "N/A"
	Unknown State = "UNKNOWN"
)

// Valid reports whether the state belongs to the normalized four-state model.
func (s State) Valid() bool {
	switch s {
	case In, Out, NotApp, Unknown:
		return true
	default:
		return false
	}
}

type ProviderID string
type InstanceID string

// Explanation describes why a provider returned a state. Code is stable
// machine-readable data. Summary is concise; evidence and action are optional.
type Explanation struct {
	Code     string `json:"code"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence,omitempty"`
	Action   string `json:"action,omitempty"`
}

// Valid reports whether the explanation uses the normalized vocabulary.
func (e Explanation) Valid() bool {
	return vocabulary.ValidCode(e.Code) && e.Summary != ""
}

// ProvenanceMethod identifies how the provider established a result.
type ProvenanceMethod string

const (
	SafeNative  ProvenanceMethod = "safe-native"
	SafeParser  ProvenanceMethod = "safe-parser"
	Unavailable ProvenanceMethod = "unavailable"
)

// Valid reports whether the method belongs to the normalized provenance model.
func (m ProvenanceMethod) Valid() bool {
	switch m {
	case SafeNative, SafeParser, Unavailable:
		return true
	default:
		return false
	}
}

// Provenance identifies how the provider established the result.
type Provenance struct {
	Method    ProvenanceMethod `json:"method"`
	Tool      string           `json:"tool,omitempty"`
	Reference string           `json:"reference,omitempty"`
}

// Valid reports whether provenance is consistent with the result state.
func (p Provenance) Valid(state State) bool {
	if !p.Method.Valid() {
		return false
	}
	switch p.Method {
	case SafeNative:
		return vocabulary.ValidIdentifier(p.Tool)
	case SafeParser:
		return p.Tool == ""
	case Unavailable:
		return state == Unknown && (p.Tool == "" || vocabulary.ValidIdentifier(p.Tool)) && (p.Tool != "" || p.Reference != "")
	default:
		return false
	}
}

// Result is one provider instance's fact about one path.
type Result struct {
	Path        Path        `json:"path"`
	Provider    ProviderID  `json:"provider"`
	Instance    InstanceID  `json:"instance"`
	State       State       `json:"state"`
	Explanation Explanation `json:"explanation"`
	Provenance  Provenance  `json:"provenance"`
}
