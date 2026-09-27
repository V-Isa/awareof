package codeowners

import "testing"

func TestPatternDecisionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		matches []string
		misses  []string
	}{
		{name: "basename anywhere", pattern: "foo", matches: []string{"foo", "foo/bar", "bar/foo", "bar/foo/baz"}, misses: []string{"foo.txt", "bar/food"}},
		{name: "root literal", pattern: "/foo", matches: []string{"foo", "foo/bar"}, misses: []string{"bar/foo", "fool"}},
		{name: "directory anywhere", pattern: "apps/", matches: []string{"apps/file", "nested/apps/file"}, misses: []string{"apps", "nested/application/file"}},
		{name: "root directory", pattern: "/docs/", matches: []string{"docs/file", "docs/deep/file"}, misses: []string{"docs", "nested/docs/file"}},
		{name: "one level wildcard", pattern: "docs/*", matches: []string{"docs/file"}, misses: []string{"docs/deep/file", "nested/docs/file"}},
		{name: "recursive logs", pattern: "**/logs", matches: []string{"logs", "logs/file", "build/logs", "deep/build/logs/file"}, misses: []string{"logs-old/file"}},
		{name: "middle recursive", pattern: "src/**/index.go", matches: []string{"src/index.go", "src/a/index.go", "src/a/b/index.go"}, misses: []string{"nested/src/index.go", "src/index.ts"}},
		{name: "question", pattern: "file?.go", matches: []string{"file1.go", "nested/fileA.go"}, misses: []string{"file.go", "file12.go"}},
		{name: "character range is literal", pattern: "file[ab].go", matches: []string{"file[ab].go"}, misses: []string{"filea.go", "fileb.go"}},
		{name: "escaped wildcard", pattern: `file\*.go`, matches: []string{"file*.go"}, misses: []string{"fileA.go"}},
		{name: "dotfile", pattern: "*", matches: []string{".env", ".github/CODEOWNERS"}},
		{name: "unicode literal", pattern: "資料/*.md", matches: []string{"資料/readme.md"}, misses: []string{"资料/readme.md"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			matcher, err := compilePattern(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range test.matches {
				if !matcher.MatchString(name) {
					t.Errorf("pattern %q did not match %q", test.pattern, name)
				}
			}
			for _, name := range test.misses {
				if matcher.MatchString(name) {
					t.Errorf("pattern %q unexpectedly matched %q", test.pattern, name)
				}
			}
		})
	}
}

func TestInvalidPatterns(t *testing.T) {
	t.Parallel()
	tests := []string{"", "!secret", "***/*.go", "foo//bar", `foo\`}
	for _, pattern := range tests {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			if _, err := compilePattern(pattern); err == nil {
				t.Fatalf("compilePattern(%q) error = nil", pattern)
			}
		})
	}
	matcher, err := compilePattern("/")
	if err != nil {
		t.Fatal(err)
	}
	if matcher.MatchString("anything") {
		t.Fatal("root-only pattern unexpectedly matched a path")
	}
}
