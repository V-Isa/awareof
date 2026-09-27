package gitexec

import "testing"

func TestEnvironmentReturnsIndependentValues(t *testing.T) {
	t.Parallel()
	first := Environment()
	first["GIT_PAGER"] = "unsafe"
	if got := Environment()["GIT_PAGER"]; got != "cat" {
		t.Fatalf("Environment()[GIT_PAGER] = %q, want cat", got)
	}

	unset := UnsetEnvironment()
	unset[0] = "changed"
	if got := UnsetEnvironment()[0]; got != "GIT_ALTERNATE_OBJECT_DIRECTORIES" {
		t.Fatalf("UnsetEnvironment()[0] = %q", got)
	}
}
