// Package gitexec defines the shared safety boundary for native Git commands.
package gitexec

// Environment returns deterministic, non-interactive settings for read-only
// Git inspection. The caller owns the returned map.
func Environment() map[string]string {
	return map[string]string{
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_PAGER":           "cat",
		"GIT_TERMINAL_PROMPT": "0",
		"LC_ALL":              "C",
		"PAGER":               "cat",
	}
}

// UnsetEnvironment returns Git variables that can redirect repository state,
// select repository-controlled executables, or write trace output. The caller
// owns the returned slice.
func UnsetEnvironment() []string {
	return []string{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR",
		"GIT_CONFIG",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_PARAMETERS",
		"GIT_DIR",
		"GIT_DIFF_OPTS",
		"GIT_EXEC_PATH",
		"GIT_EXTERNAL_DIFF",
		"GIT_INDEX_FILE",
		"GIT_LITERAL_PATHSPECS",
		"GIT_GLOB_PATHSPECS",
		"GIT_NOGLOB_PATHSPECS",
		"GIT_ICASE_PATHSPECS",
		"GIT_OBJECT_DIRECTORY",
		"GIT_REDIRECT_STDERR",
		"GIT_TRACE",
		"GIT_TRACE2",
		"GIT_TRACE2_EVENT",
		"GIT_TRACE2_PERF",
		"GIT_TRACE_CURL",
		"GIT_TRACE_CURL_NO_DATA",
		"GIT_TRACE_PACK_ACCESS",
		"GIT_TRACE_PACKET",
		"GIT_TRACE_PERFORMANCE",
		"GIT_TRACE_REFS",
		"GIT_TRACE_SETUP",
		"GIT_TRACE_SHALLOW",
		"GIT_WORK_TREE",
	}
}
