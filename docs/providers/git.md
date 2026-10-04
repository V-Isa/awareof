# Git provider

Provider ID: `git`

## Question answered

For the detected Git worktree:

> Does this repository path belong to Git's effective tracking scope?

This is broader than asking whether a path is already tracked. An untracked path is `IN` when Git would normally allow it to be added.

## States

- `IN`: the path is tracked, or it is untracked and not ignored.
- `OUT`: the path is untracked and effectively ignored.
- `N/A`: the path is below a tracked submodule and therefore belongs to the nested worktree, not the outer worktree. The submodule entry itself is `IN`.
- `UNKNOWN`: the required Git executable is missing, changed, unapproved, or otherwise unavailable through the safe execution boundary.

A tracked path remains `IN` when it also matches an ignore rule. The explanation reports both facts. A negated ignore rule can make an untracked path `IN`.

## Discovery and evaluation

The provider detects one worktree instance when the repository root contains a `.git` entry. It is not detected outside a Git worktree.

Evaluation uses an approved Git executable with fixed, batched operations equivalent to:

```text
git ls-files --cached --stage --full-name -z
git check-ignore --no-index --verbose --non-matching --stdin -z
```

`--no-index` lets `awareof` explain ignore rules that also match tracked paths without mistaking those paths for `OUT`.

Git supplies its effective ignore behavior, including repository `.gitignore` files, `.git/info/exclude`, and configured global excludes. Explanations identify the matching source, line, and pattern. An absolute global-excludes path is not exposed; the explanation calls it `global Git excludes`.

Existing files, nonexistent logical paths, and symlink entries are evaluated by repository-relative path. `awareof` does not follow a queried symlink outside the repository.

## Safety and limits

Discovery does not execute Git. Evaluation requires explicit approval tied to the executable's resolved path and SHA-256 digest.

`awareof` invokes Git without a shell, disables filesystem monitoring and optional locks, disables prompts and pagers, and removes Git environment variables that can redirect repository state, select executables, or write trace output. The shared timeout, output bounds, and process containment also apply.

An unavailable approved mechanism produces `UNKNOWN`. A failed Git command or malformed native output is an operational error, not an `OUT` result.

The provider evaluates the current worktree and index. It does not answer whether a path exists in another commit, is present on a remote, or will be included by `git archive`. Archive membership, including `.gitattributes` `export-ignore`, is a separate roadmap direction.

## Examples

```text
.env
  git  OUT  ignored by .gitignore:1 pattern ".env"

tracked.env
  git  IN   tracked; also matches .gitignore:1 pattern ".env"

new.go
  git  IN   not ignored; eligible for tracking
```

## Validation strategy

Unit decision tables cover tracked, ignored, negated, ordinary, submodule, unavailable-tool, malformed-output, and error behavior. Native-parity tests run real Git in disposable fixture repositories inside the hardened integration container.

## Sources

- [Git: `gitignore`](https://git-scm.com/docs/gitignore)
- [Git: `git check-ignore`](https://git-scm.com/docs/git-check-ignore)
- [Git: `git ls-files`](https://git-scm.com/docs/git-ls-files)
