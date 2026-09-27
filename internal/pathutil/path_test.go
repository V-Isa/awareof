package pathutil

import (
	"path/filepath"
	"testing"
)

func TestRelativeWithin(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "repo")
	tests := []struct {
		name   string
		target string
		want   string
		inside bool
	}{
		{name: "root", target: root, want: ".", inside: true},
		{name: "child", target: filepath.Join(root, "src", "main.go"), want: filepath.Join("src", "main.go"), inside: true},
		{name: "sibling prefix", target: root + "-other", inside: false},
		{name: "parent", target: filepath.Dir(root), inside: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := RelativeWithin(root, test.target)
			if (err == nil) != test.inside {
				t.Fatalf("RelativeWithin() error = %v, inside = %v", err, test.inside)
			}
			if got != test.want {
				t.Fatalf("RelativeWithin() = %q, want %q", got, test.want)
			}
			if Within(root, test.target) != test.inside {
				t.Fatalf("Within() = %v, want %v", Within(root, test.target), test.inside)
			}
		})
	}
}
