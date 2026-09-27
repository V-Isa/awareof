package vocabulary

import "testing"

func TestValidCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "one segment", value: "unknown", want: true},
		{name: "multiple segments", value: "tool/not-approved", want: true},
		{name: "digits", value: "provider/v2", want: true},
		{name: "empty"},
		{name: "uppercase", value: "Tool/error"},
		{name: "empty segment", value: "tool//error"},
		{name: "leading slash", value: "/tool/error"},
		{name: "trailing slash", value: "tool/error/"},
		{name: "leading hyphen", value: "tool/-error"},
		{name: "trailing hyphen", value: "tool/error-"},
		{name: "underscore", value: "tool/not_approved"},
		{name: "non-ASCII", value: "tool/café"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidCode(test.value); got != test.want {
				t.Fatalf("ValidCode(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestValidIdentifier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  bool
	}{
		{value: "git", want: true},
		{value: "github-actions", want: true},
		{value: "typescript7", want: true},
		{value: ""},
		{value: "Git"},
		{value: "7zip"},
		{value: "git/tool"},
		{value: "git_unsafe"},
		{value: "git-"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			if got := ValidIdentifier(test.value); got != test.want {
				t.Fatalf("ValidIdentifier(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
