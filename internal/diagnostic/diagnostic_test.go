package diagnostic

import "testing"

func TestDiagnosticValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		diagnostic Diagnostic
		want       bool
	}{
		{name: "error", diagnostic: Diagnostic{Level: Error, Code: "tool/not-approved", Summary: "tool is not approved"}, want: true},
		{name: "warning", diagnostic: Diagnostic{Level: Warning, Code: "provider/limited", Summary: "provider has limited support"}, want: true},
		{name: "info", diagnostic: Diagnostic{Level: Info, Code: "tool/approved", Summary: "tool is approved"}, want: true},
		{name: "empty level", diagnostic: Diagnostic{Code: "tool/error", Summary: "failed"}},
		{name: "invented level", diagnostic: Diagnostic{Level: "NOTICE", Code: "tool/error", Summary: "failed"}},
		{name: "empty code", diagnostic: Diagnostic{Level: Error, Summary: "failed"}},
		{name: "empty summary", diagnostic: Diagnostic{Level: Error, Code: "tool/error"}},
		{name: "uppercase code", diagnostic: Diagnostic{Level: Error, Code: "Tool/error", Summary: "failed"}},
		{name: "empty segment", diagnostic: Diagnostic{Level: Error, Code: "tool//error", Summary: "failed"}},
		{name: "leading slash", diagnostic: Diagnostic{Level: Error, Code: "/tool/error", Summary: "failed"}},
		{name: "trailing slash", diagnostic: Diagnostic{Level: Error, Code: "tool/error/", Summary: "failed"}},
		{name: "leading hyphen", diagnostic: Diagnostic{Level: Error, Code: "tool/-error", Summary: "failed"}},
		{name: "trailing hyphen", diagnostic: Diagnostic{Level: Error, Code: "tool/error-", Summary: "failed"}},
		{name: "underscore", diagnostic: Diagnostic{Level: Error, Code: "tool/not_approved", Summary: "failed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.diagnostic.Valid(); got != test.want {
				t.Fatalf("Diagnostic.Valid() = %v, want %v", got, test.want)
			}
		})
	}
}
