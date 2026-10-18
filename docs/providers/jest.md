# Jest provider

## Question

The provider answers one repository-level question:

> Is this repository path an effective test file in at least one detected Jest context?

This is test-file membership. It is not whether a test passes, whether source code is exercised, related-test selection, coverage, package-script selection, or an arbitrary Jest CLI invocation.

## States

- `IN`: at least one resolved context selects the path as a test file.
- `OUT`: every applicable resolved context does not select the path.
- `UNKNOWN`: no context proves `IN`, and at least one applicable context cannot be evaluated authoritatively.
- `N/A`: no detected Jest context applies to the path.

Detected contexts remain evidence. One repository-level result uses union semantics: one resolved `IN` takes precedence over another context's `OUT` or `UNKNOWN`.

A nonexistent repository-relative query is `OUT` when every applicable context resolves because Jest cannot select a file that is absent.

## Context discovery

A package directory is a context when its regular `package.json` directly declares Jest in `dependencies`, `devDependencies`, or `optionalDependencies`, or contains a `jest` field. A supported config at the repository root also creates a root context.

Discovery skips version-control metadata and `node_modules`. It reads manifests and config names but does not load Jest or execute JavaScript. An unreadable entry or malformed manifest creates uncertainty for that subtree. An incomplete scan creates repository-wide uncertainty.

Each context uses its package directory as the modeled working directory. Its installed `node_modules/jest` package may resolve from that directory or a repository ancestor. The package and fixed `bin/jest.js` entry must remain inside the inspected repository.

Supported config names depend on the installed Jest version:

```text
Jest 29: jest.config.js, .ts, .mjs, .cjs, .json
Jest 30: the Jest 29 names plus .mts and .cts
```

A `package.json` `jest` object is supported. With Jest 30, a string form may reference a repository-contained JSON file. Jest 29 does not support that string form. Multiple supported configs for one context are ambiguous and return `UNKNOWN`.

When no config exists, `awareof` supplies only a controlled `rootDir` equal to the context root and lets the exact installed Jest version apply its defaults.

A configless package below a configured context does not create a second context. The configured ancestor already supplies the repository-level membership fact for those paths.

## Explicit delegation

Effective Jest discovery can execute repository-controlled configuration, presets, resolvers, sequencers, and their dependencies. Normal inspection therefore returns `UNKNOWN` without executing them:

```text
jest  UNKNOWN  effective Jest test discovery requires repository code execution
```

For a repository you already trust, delegate this authority for one invocation:

```sh
awareof --allow-repo-code=jest '**/*.test.ts'
```

This permission is provider-specific, invocation-only, nonpersistent, and separate from native-tool approval. `.awareof.yaml` cannot grant it. Without permission, a Jest contract remains valid but validation is unresolved.

Delegation is not a sandbox. Repository code runs with the user's privileges. It may access files, environment data and credentials, the network, or processes. Delegated results are only as trustworthy as the repository code allowed to run.

## Native evaluation

The provider supports stable Jest 29 and 30. It uses an approved external native executable file named `node` and the repository's installed Jest entry point with this fixed operation:

```text
node <project-jest-bin>
  --config <explicit-config>
  --listTests
  --json
  --no-cache
  --no-watchman
```

It does not use a shell, package script, `.bin/jest`, npm, or npx, and it does not start a test run. The complete successful JSON list is authoritative. Duplicate normalized paths are harmless set entries. Failed, partial, relative, or malformed output is never treated as complete membership.

Execution remains bounded and removes Node loader, inspector, report, coverage, and protocol variables that could alter the evaluator. Other caller variables and credentials remain visible because delegated configuration can depend on the invocation environment.

Node approval permits starting Node. `--allow-repo-code=jest` permits loading the project Jest package and repository-controlled discovery graph. Neither gate implies the other.

## Repository boundary

The initial config entry must be a regular repository file and is always passed explicitly. Jest cannot discover a config above the inspected repository.

The delegated config may itself load files outside the repository. That behavior is part of the explicit unsandboxed grant; `awareof` does not claim to confine arbitrary JavaScript imports.

## Normal `UNKNOWN` cases

The provider returns `UNKNOWN` when, for example:

- repository-code delegation was not supplied;
- Node is missing, unapproved, repository-controlled, or not a recognized native executable file named `node`;
- Jest is missing, outside the repository, unsupported, or lacks its fixed entry point;
- config selection is ambiguous, unreadable, nonregular, or outside the repository;
- a queried path contains a symbolic link or cannot be inspected safely;
- discovery is incomplete;
- Jest exits unsuccessfully, times out, exceeds output bounds, or returns invalid output.

An unsuccessful evaluation never turns partial output into `IN` or `OUT`. Path-specific uncertainty stays local where possible.

## Known limits

- Only stable Jest 29 and 30 with a repository-contained `node_modules/jest` installation are supported.
- Yarn Plug'n'Play and external package installations are unresolved.
- Nested configs without a direct package context are not discovered automatically.
- Custom CLI filters such as `--runTestsByPath`, `--findRelatedTests`, `--onlyChanged`, `--selectProjects`, and positional patterns are outside this provider question.
- Directory queries are expanded by the core path source. A directory passed through as a literal provider path is `OUT` because membership applies to test files.
- One context is evaluated in one process under the shared timeout and output bounds. Large or slow configuration graphs may return `UNKNOWN`.
- Discovery reads at most 100,000 repository entries. Reaching that limit makes repository-wide Jest membership unresolved.

## Sources

- [Jest CLI `--listTests`](https://jestjs.io/docs/cli#--listtests)
- [Jest configuration](https://jestjs.io/docs/configuration)
- [Jest 29.7 configuration](https://jestjs.io/docs/29.7/configuration)
