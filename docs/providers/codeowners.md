# CODEOWNERS provider

Provider ID: `codeowners`

## Question answered

For each repository path:

> Does the final matching valid rule in the effective local GitHub CODEOWNERS file declare one or more owner references for this path?

`IN` means that the final matching valid rule declares at least one owner reference. `OUT` means that no valid rule matches, or that the final matching rule intentionally has no owners.

Owner references are explanatory data. The provider does not contact GitHub. It cannot verify that a user or team exists, is visible, or has write access. It also does not inspect branch protection. An `IN` result does not promise that GitHub will request or require a review.

## Discovery

GitHub searches these repository-root-relative locations in order and uses the first CODEOWNERS file it finds:

1. `.github/CODEOWNERS`
2. `CODEOWNERS`
3. `docs/CODEOWNERS`

`awareof` applies the same precedence to the current worktree. It does not merge rules from multiple locations. A directory at one of these paths is not a CODEOWNERS file.

The provider is not detected when none of these files exists. A symlink or another non-regular CODEOWNERS file produces `UNKNOWN`. `awareof` cannot inspect it safely or establish GitHub's behavior.

## Matching

Matching is repository-root-relative and case-sensitive on every operating system. The provider implements the documented GitHub CODEOWNERS pattern rules, separately from `awareof` query globs:

- `*` and `?` do not cross `/`;
- `**` matches recursively;
- a leading `/`, or a slash other than a trailing `/`, roots a pattern at the repository root;
- a pattern with no `/` can match at any depth;
- a trailing `/` covers files below the matched directory;
- square brackets are literal and do not define character ranges;
- `!` negation is unsupported;
- the last valid matching rule wins;
- a final matching rule with no owners removes coverage from a broader rule.

Rules apply to logical repository paths. Existing files, nonexistent paths, and symlink entries are matched by their repository path name; the provider does not follow queried symlinks.

GitHub skips invalid lines, so `awareof` does the same. When no valid rule covers a path, the explanation notes any skipped invalid lines. Inline comments and escaped spaces are supported. GitHub treats a leading `#` as a comment even when a backslash precedes it, so `awareof` does the same.

## Size and uncertainty

GitHub loads a CODEOWNERS file only when it is smaller than 3 MB. At or above that limit, every path is `OUT`. The explanation states that GitHub does not load the file.

The provider returns `UNKNOWN` when the selected file cannot be read safely or is not valid UTF-8. It never executes a native tool or makes a network request.

## Validation strategy

GitHub provides no supported executable or local API for an arbitrary worktree. Tests therefore use GitHub's documented examples. They cover location precedence, the last matching rule, ownerless rules, invalid lines, directory recursion, unsupported negation, literal character ranges, comments, escaping, case sensitivity, and the 3 MB limit.

GitHub's REST endpoint can validate CODEOWNERS errors on a remote branch. It cannot evaluate uncommitted local worktree matching, so inspection does not use it.

One evaluation has a deterministic rule-comparison limit. This bounds work on untrusted repositories. A path that cannot complete within the limit is `UNKNOWN`. A smaller path query can evaluate the same rules with less work. Cancellation is an operational error, not a scope state.

## Sources

- [GitHub: About code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
- [GitHub REST API: List CODEOWNERS errors](https://docs.github.com/en/rest/repos/repos#list-codeowners-errors)
- [Community compatibility reference: `hmarr/codeowners`](https://github.com/hmarr/codeowners) (not authoritative)
