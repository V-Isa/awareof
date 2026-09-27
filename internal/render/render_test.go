package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/contract"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestJSON(t *testing.T) {
	t.Parallel()

	t.Run("empty slices are arrays", func(t *testing.T) {
		t.Parallel()
		var output bytes.Buffer
		if err := JSON(&output, nil, nil); err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"schemaVersion\": 1,\n  \"paths\": [],\n  \"results\": []\n}\n"
		if output.String() != want {
			t.Fatalf("JSON() = %q, want %q", output.String(), want)
		}
	})

	t.Run("result document", func(t *testing.T) {
		t.Parallel()
		var output bytes.Buffer
		results := []scope.Result{{
			Path:        ".env",
			Provider:    "git",
			Instance:    "git",
			State:       scope.Out,
			Explanation: scope.Explanation{Code: "git/ignored", Summary: "ignored"},
			Provenance:  scope.Provenance{Method: "safe-native", Tool: "git"},
		}}
		if err := JSON(&output, []scope.Path{".env"}, results); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{`"schemaVersion": 1`, `"path": ".env"`, `"state": "OUT"`, `"method": "safe-native"`} {
			if !strings.Contains(output.String(), fragment) {
				t.Errorf("JSON output %q does not contain %q", output.String(), fragment)
			}
		}
	})

	t.Run("writer error", func(t *testing.T) {
		t.Parallel()
		if err := JSON(errorWriter{}, nil, nil); err == nil || !strings.Contains(err.Error(), "encode JSON output") {
			t.Fatalf("JSON() error = %v, want wrapped writer error", err)
		}
	})
}

func TestValidationJSON(t *testing.T) {
	t.Parallel()
	t.Run("empty report", func(t *testing.T) {
		t.Parallel()
		var output bytes.Buffer
		if err := ValidationJSON(&output, contract.Report{Contract: ".awareof.yaml"}); err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"schemaVersion\": 1,\n  \"valid\": true,\n  \"contract\": \".awareof.yaml\",\n  \"checks\": []\n}\n"
		if output.String() != want {
			t.Fatalf("ValidationJSON() = %q, want %q", output.String(), want)
		}
	})

	t.Run("failed report", func(t *testing.T) {
		t.Parallel()
		var output bytes.Buffer
		report := contract.Report{Contract: ".awareof.yaml", Checks: []contract.Check{{
			Path: ".env", Pattern: ".env*", Provider: "git", Expected: scope.Out, Status: contract.Mismatch,
			Actual: []scope.Result{{Path: ".env", Provider: "git", Instance: "git", State: scope.In}},
		}}}
		if err := ValidationJSON(&output, report); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{`"valid": false`, `"pattern": ".env*"`, `"status": "MISMATCH"`} {
			if !strings.Contains(output.String(), fragment) {
				t.Errorf("ValidationJSON() = %q, want %q", output.String(), fragment)
			}
		}
	})

	t.Run("writer error", func(t *testing.T) {
		t.Parallel()
		if err := ValidationJSON(errorWriter{}, contract.Report{}); err == nil || !strings.Contains(err.Error(), "encode validation JSON output") {
			t.Fatalf("ValidationJSON() error = %v", err)
		}
	})
}

func TestValidationHuman(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		report contract.Report
		want   string
	}{
		{name: "no matching paths", report: contract.Report{}, want: "Validation passed: no paths matched contract rules.\n"},
		{name: "success", report: contract.Report{Checks: []contract.Check{{Status: contract.Satisfied}}}, want: "Validation passed: 1 assertions satisfied.\n"},
		{name: "provider absent", report: contract.Report{Checks: []contract.Check{{
			Path: "a", Pattern: "*", Provider: "git", Expected: scope.In, Status: contract.Unresolved, Actual: []scope.Result{},
		}}}, want: "Validation failed.\na\n  git expected IN, actual N/A (rule \"*\")\n    provider not detected or applicable\n"},
		{name: "mixed instances and safe text", report: contract.Report{Checks: []contract.Check{
			{Path: "a\n", Pattern: "**", Provider: "docker", Expected: scope.In, Status: contract.Mismatch, Actual: []scope.Result{
				{Provider: "docker", Instance: "web", State: scope.In, Explanation: scope.Explanation{Summary: "included"}},
				{Provider: "docker", Instance: "worker", State: scope.Out, Explanation: scope.Explanation{Summary: "excluded\nunsafe"}},
			}},
			{Path: "b", Pattern: "b", Provider: "git", Expected: scope.Out, Status: contract.Unresolved, Actual: []scope.Result{
				{Provider: "git", Instance: "git", State: scope.Unknown, Explanation: scope.Explanation{Summary: "cannot know"}},
			}},
		}}, want: "Validation failed.\n\"a\\n\"\n  docker expected IN, actual MIXED (rule \"**\")\n    docker/web: included\n    docker/worker: \"excluded\\nunsafe\"\nb\n  git expected OUT, actual UNKNOWN (rule \"b\")\n    git: cannot know\n"},
		{name: "setup requirement is aggregated", report: contract.Report{Checks: []contract.Check{
			{Path: "a", Pattern: "**", Provider: "git", Expected: scope.Out, Status: contract.Unresolved, Actual: []scope.Result{{
				Provider: "git", Instance: "git", State: scope.Unknown,
				Explanation: scope.Explanation{Code: "tool/not-approved", Summary: "tool \"git\" is not approved", Evidence: "/usr/bin/git"},
				Provenance:  scope.Provenance{Method: scope.Unavailable, Tool: "git"},
			}}},
			{Path: "b", Pattern: "**", Provider: "git", Expected: scope.Out, Status: contract.Unresolved, Actual: []scope.Result{{
				Provider: "git", Instance: "git", State: scope.Unknown,
				Explanation: scope.Explanation{Code: "tool/not-approved", Summary: "tool \"git\" is not approved", Evidence: "/usr/bin/git"},
				Provenance:  scope.Provenance{Method: scope.Unavailable, Tool: "git"},
			}}},
		}}, want: "Validation failed.\na\n  git expected OUT, actual UNKNOWN (rule \"**\")\n    git: UNKNOWN\nb\n  git expected OUT, actual UNKNOWN (rule \"**\")\n    git: UNKNOWN\n\n1 native tool requires setup.\n  git: tool \"git\" is not approved\n    evidence: /usr/bin/git\nRun awareof --setup (with the same --tool selections, if any).\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := ValidationHuman(&output, test.report); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("ValidationHuman() = %q, want %q", output.String(), test.want)
			}
		})
	}

	t.Run("writer errors", func(t *testing.T) {
		t.Parallel()
		for _, report := range []contract.Report{
			{},
			{Checks: []contract.Check{{Status: contract.Satisfied}}},
			{Checks: []contract.Check{{Path: "a", Provider: "git", Expected: scope.In, Status: contract.Unresolved}}},
		} {
			if err := ValidationHuman(errorWriter{}, report); err == nil || !strings.Contains(err.Error(), "write validation output") {
				t.Fatalf("ValidationHuman() error = %v", err)
			}
		}
	})
}

func TestHuman(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		paths   []scope.Path
		results []scope.Result
		want    string
	}{
		{name: "no paths", want: "No paths matched.\n"},
		{name: "no providers", paths: []scope.Path{"file.go"}, want: "No providers detected.\n"},
		{
			name:  "provider and instance rows",
			paths: []scope.Path{"a", "b"},
			results: []scope.Result{
				{Path: "a", Provider: "docker", Instance: "web", State: scope.In, Explanation: scope.Explanation{Summary: "included"}},
				{Path: "a", Provider: "git", Instance: "git", State: scope.Out, Explanation: scope.Explanation{Summary: "ignored"}},
				{Path: "b", Provider: "git", Instance: "git", State: scope.In, Explanation: scope.Explanation{Summary: "tracked"}},
			},
			want: "a\n  docker/web       IN      included\n  git              OUT     ignored\nb\n  git              IN      tracked\n",
		},
		{
			name:  "results need not be sorted by path",
			paths: []scope.Path{"a", "b"},
			results: []scope.Result{
				{Path: "b", Provider: "git", Instance: "git", State: scope.In, Explanation: scope.Explanation{Summary: "tracked"}},
				{Path: "a", Provider: "git", Instance: "git", State: scope.Out, Explanation: scope.Explanation{Summary: "ignored"}},
			},
			want: "a\n  git              OUT     ignored\nb\n  git              IN      tracked\n",
		},
		{
			name:  "native setup cause is shown once",
			paths: []scope.Path{"a", "b"},
			results: []scope.Result{
				{
					Path: "a", Provider: "git", Instance: "git", State: scope.Unknown,
					Explanation: scope.Explanation{Code: "tool/not-approved", Summary: "tool \"git\" is not approved", Evidence: "resolved executable /usr/bin/git"},
					Provenance:  scope.Provenance{Method: scope.Unavailable, Tool: "git"},
				},
				{
					Path: "b", Provider: "eas", Instance: "eas", State: scope.Unknown,
					Explanation: scope.Explanation{Code: "tool/not-approved", Summary: "tool \"git\" is not approved", Evidence: "resolved executable /usr/bin/git"},
					Provenance:  scope.Provenance{Method: scope.Unavailable, Tool: "git"},
				},
				{
					Path: "b", Provider: "git", Instance: "git", State: scope.Unknown,
					Explanation: scope.Explanation{Code: "tool/not-approved", Summary: "tool \"git\" is not approved", Evidence: "resolved executable /usr/bin/git"},
					Provenance:  scope.Provenance{Method: scope.Unavailable, Tool: "git"},
				},
			},
			want: "a\n" +
				"  git              UNKNOWN\n" +
				"b\n" +
				"  eas              UNKNOWN\n" +
				"  git              UNKNOWN\n" +
				"\n1 native tool requires setup.\n" +
				"  git: tool \"git\" is not approved\n" +
				"    evidence: resolved executable /usr/bin/git\n" +
				"Run awareof --setup (with the same --tool selections, if any).\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := Human(&output, test.paths, test.results); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("Human() = %q, want %q", output.String(), test.want)
			}
		})
	}

	t.Run("writer error", func(t *testing.T) {
		t.Parallel()
		if err := Human(errorWriter{}, []scope.Path{"a"}, []scope.Result{{Path: "a"}}); err == nil || !strings.Contains(err.Error(), "write human output") {
			t.Fatalf("Human() error = %v, want wrapped writer error", err)
		}
	})

	t.Run("unexpected result path", func(t *testing.T) {
		t.Parallel()
		var output bytes.Buffer
		err := Human(&output, []scope.Path{"a"}, []scope.Result{{Path: "b"}})
		if err == nil || !strings.Contains(err.Error(), "was not requested") {
			t.Fatalf("Human() error = %v, want unexpected path error", err)
		}
		if output.Len() != 0 {
			t.Fatalf("Human() wrote partial output %q", output.String())
		}
	})
}

func TestSafeText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "printable", value: "src/cafe.go", want: "src/cafe.go"},
		{name: "newline", value: "bad\nname", want: `"bad\nname"`},
		{name: "escape", value: "bad\x1b[31m", want: `"bad\x1b[31m"`},
		{name: "bidi control", value: "bad\u202ename", want: `"bad\u202ename"`},
		{name: "invalid utf8", value: string([]byte{'b', 0xff}), want: `"b\xff"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := SafeText(test.value); got != test.want {
				t.Fatalf("SafeText(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
