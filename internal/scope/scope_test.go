package scope

import "testing"

func TestStateValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state State
		want  bool
	}{
		{name: "in", state: In, want: true},
		{name: "out", state: Out, want: true},
		{name: "not applicable", state: NotApp, want: true},
		{name: "unknown", state: Unknown, want: true},
		{name: "empty", state: "", want: false},
		{name: "invented state", state: "MIXED", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.state.Valid(); got != test.want {
				t.Fatalf("State(%q).Valid() = %v, want %v", test.state, got, test.want)
			}
		})
	}
}

func TestProvenanceMethodValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method ProvenanceMethod
		want   bool
	}{
		{name: "safe native", method: SafeNative, want: true},
		{name: "safe parser", method: SafeParser, want: true},
		{name: "unavailable", method: Unavailable, want: true},
		{name: "empty", method: ""},
		{name: "invented", method: "unsafe-native"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.method.Valid(); got != test.want {
				t.Fatalf("ProvenanceMethod(%q).Valid() = %v, want %v", test.method, got, test.want)
			}
		})
	}
}

func TestExplanationValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		explanation Explanation
		want        bool
	}{
		{name: "valid", explanation: Explanation{Code: "git/not-ignored", Summary: "not ignored"}, want: true},
		{name: "empty code", explanation: Explanation{Summary: "not ignored"}},
		{name: "invalid code", explanation: Explanation{Code: "Git/ignored", Summary: "ignored"}},
		{name: "empty summary", explanation: Explanation{Code: "git/ignored"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.explanation.Valid(); got != test.want {
				t.Fatalf("Explanation.Valid() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProvenanceValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		provenance Provenance
		state      State
		want       bool
	}{
		{name: "native", provenance: Provenance{Method: SafeNative, Tool: "git"}, state: In, want: true},
		{name: "native with reference", provenance: Provenance{Method: SafeNative, Tool: "git", Reference: "EAS CLI 23.2.0"}, state: In, want: true},
		{name: "native without tool", provenance: Provenance{Method: SafeNative}, state: In},
		{name: "native with invalid tool", provenance: Provenance{Method: SafeNative, Tool: "Git/tool"}, state: In},
		{name: "parser", provenance: Provenance{Method: SafeParser}, state: Out, want: true},
		{name: "parser with reference", provenance: Provenance{Method: SafeParser, Reference: "moby/patternmatcher"}, state: Out, want: true},
		{name: "parser with tool", provenance: Provenance{Method: SafeParser, Tool: "git"}, state: Out},
		{name: "unavailable unknown", provenance: Provenance{Method: Unavailable, Tool: "git"}, state: Unknown, want: true},
		{name: "unavailable parser unknown", provenance: Provenance{Method: Unavailable, Reference: "EAS CLI 23.2.0"}, state: Unknown, want: true},
		{name: "unavailable known", provenance: Provenance{Method: Unavailable, Tool: "git"}, state: In},
		{name: "unavailable without tool", provenance: Provenance{Method: Unavailable}, state: Unknown},
		{name: "invalid method", provenance: Provenance{Method: "native", Tool: "git"}, state: In},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.provenance.Valid(test.state); got != test.want {
				t.Fatalf("Provenance.Valid(%s) = %v, want %v", test.state, got, test.want)
			}
		})
	}
}
