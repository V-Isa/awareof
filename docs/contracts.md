# Repository contracts

`awareof --validate` compares effective provider state with sparse repository assertions in `.awareof.yaml`:

```yaml
version: 1

rules:
  ".env*":
    git: out
    docker: out

  "dist/**":
    git: out
    eas: in

  "src/**":
    git: in
```

The root shape is fixed. `version` and `rules` are required. Unknown fields are errors, and only version `1` is supported. A repository can use `.awareof.yml` instead. Using both file names is an error.

Each rule is a repository-root-relative pattern in the same glob language as CLI queries. The language supports `*`, `**`, `?`, and character classes. Dotfiles are ordinary path entries. Brace expansion, absolute paths, and paths that escape the repository are rejected. YAML aliases, anchors, merge keys, and custom tags are also rejected.

Assertions are partial. Only registered provider IDs may be named, and the only allowed expectations are lowercase `in` and `out`. Provider configuration is not duplicated in this file.

## Matching and overlap

Without a path selector, validation scans the entire repository from its root. Positional paths, globs, stdin, `--staged`, and `--changed-from REF` restrict the scan. Full scans include ignored and untracked files. Git change selectors include only paths reported by their documented Git diff. A rule with no matching path passes because there is nothing to validate.

A narrower rule may override a broader rule when the relationship is clear:

```yaml
rules:
  "src/**":
    git: in
  "src/generated/**":
    git: out
```

An exact path is narrower than a matching glob. A pattern below a literal trailing `/**` prefix is narrower than that prefix. A conflicting overlap is a configuration error unless one pattern is clearly narrower. YAML order never changes the result.

## Provider instances

A provider assertion requires every applicable instance to have the expected state. One contradictory instance fails the assertion, even if another instance is `UNKNOWN`. `N/A` instances are ignored. An assertion is unresolved when there is no contradiction but no applicable instance exists or a required result is `UNKNOWN`. The precedence is `MISMATCH` over `UNRESOLVED` over `SATISFIED`. All instance results remain in the details.

## Output and exits

Human output prints only failures and their provider explanations. JSON contains all evaluated checks and their underlying instance results. `--quiet` suppresses validation output but not diagnostics.

Each assertion has one contract status: `SATISFIED`, `MISMATCH`, or `UNRESOLVED`. `UNKNOWN` and the absence of an applicable provider produce `UNRESOLVED`; they do not become mismatches or operational errors. See [the normative vocabulary](terminology.md).

Exit codes are:

- `0`: every evaluated assertion is satisfied, including the zero-match case;
- `1`: at least one assertion is mismatched, unknown, or not applicable;
- `2`: usage, contract configuration, provider, or runtime error.
