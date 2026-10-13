# ESLint provider

## Question

The provider answers one repository-level question:

> Does this repository path match effective ESLint flat configuration in at least one detected ESLint context?

This is file eligibility. It is not whether the file passes lint, whether `--fix` changes it, whether a package script names it, or whether Expo lint nominates it.

## States

- `IN`: at least one resolved context selects the path with effective configuration.
- `OUT`: every applicable resolved context ignores the path or supplies no effective configuration.
- `UNKNOWN`: no context proves `IN`, and at least one applicable context cannot be evaluated authoritatively.
- `N/A`: no detected ESLint context applies to the path.

Detected contexts remain evidence. One repository-level result uses union semantics: one resolved `IN` takes precedence over another context's `OUT` or `UNKNOWN`.

## Context discovery

A package directory is a context when its regular `package.json` directly declares ESLint in `dependencies`, `devDependencies`, or `optionalDependencies`. A supported flat config at the repository root also creates a root context, even when nested package contexts exist.

Discovery recognizes ESLint's documented config precedence:

```text
eslint.config.js
eslint.config.mjs
eslint.config.cjs
eslint.config.ts
eslint.config.mts
eslint.config.cts
```

TypeScript config files also require the ESLint version's supported runtime loader. If ESLint cannot load one, evaluation returns `UNKNOWN`.

It skips only version-control metadata and `node_modules`. It does not load a config or execute JavaScript. An unreadable entry or malformed manifest creates uncertainty only for that directory subtree. An incomplete scan remains repository-wide uncertainty because undiscovered contexts may exist anywhere after the stopping point.

A declared nested package without its own config does not create a second unresolved context when a configured ancestor context already covers it. The ancestor context provides the repository-level eligibility fact for those paths.

Each context uses its direct package root as the modeled working directory. Its installed `node_modules/eslint` package may resolve from that directory or a repository ancestor. The package and fixed `lib/api.js` entry must remain inside the inspected repository.

## Explicit delegation

Effective flat configuration is executable JavaScript or TypeScript. Normal inspection therefore returns `UNKNOWN` without executing it:

```text
eslint  UNKNOWN  effective ESLint configuration requires repository code execution
```

For a repository you already trust, delegate this authority for one invocation:

```sh
awareof --allow-repo-code=eslint src/app.ts
```

This permission is provider-specific, invocation-only, nonpersistent, and separate from native-tool approval. `.awareof.yaml` cannot grant it. Without permission, an ESLint contract remains valid but validation is unresolved.

Delegation is not a sandbox. The config and modules it imports run with the user's privileges. They may access files, environment data and credentials, the network, or processes.

Delegated results are only as trustworthy as the repository code allowed to run. Diagnostic normalization and output bounds protect `awareof`'s protocol; they cannot prevent delegated code from manipulating its evaluator process or output.

## Native evaluation

The provider supports stable ESLint 9 and 10. It uses:

- an approved external native executable file named `node`;
- an awareof-owned fixed helper;
- the repository's installed ESLint `lib/api.js`;
- ESLint's public `isPathIgnored()` and `calculateConfigForFile()` APIs.

The helper passes an explicit root config and sets `cwd` to the context root. It does not use a shell, package script, `.bin/eslint`, npm, npx, Expo lint, or the network on awareof's behalf. Provider execution remains bounded and removes loader, inspector, config-mode, feature-flag, and protocol variables that could change the evaluator. Other caller variables, including `NODE_ENV` and credentials, remain visible because executable configuration can depend on the invocation environment.

Project-local ESLint is not separately approved as a native tool. Loading it is repository-code execution and requires `--allow-repo-code=eslint`. Node approval permits starting Node; delegation permits loading the project package and configuration. Neither gate implies the other.

## Repository boundary and nested configs

The selected config entry must be a regular repository file. Explicit config binding prevents ESLint from discovering an ancestor config above the inspected repository.

ESLint documents that patterns in an explicitly supplied config are relative to `cwd`. This preserves normal behavior for a config at its context root. ESLint 10 normally searches from each target file and can select nested configs. Explicitly binding those configs can change relative matching or observable `process.cwd()` behavior.

For this slice, a path below a nested `eslint.config.*` returns `UNKNOWN`. `awareof` does not guess, change working directories, or fall through to an external ancestor config. Native parity tests cover root-config `files`, `ignores`, and `basePath`, and pin the conservative nested-config result.

The delegated config may itself import files outside the repository. That behavior is part of the explicit unsandboxed grant; `awareof` does not claim to confine arbitrary JavaScript imports.

## Normal `UNKNOWN` cases

The provider returns `UNKNOWN` when, for example:

- repository-code delegation was not supplied;
- Node is missing, unapproved, repository-controlled, or not a recognized native executable file named `node`;
- ESLint is missing, outside the repository, not stable version 9 or 10, or lacks its fixed API entry;
- the root config is missing, nonregular, unreadable, or resolves outside the repository;
- a nested config controls the queried subtree;
- a queried path contains a symbolic link or cannot be inspected safely;
- discovery is incomplete;
- the evaluator fails, times out, exceeds output bounds, or returns invalid or partial output.

An unsuccessful evaluation never turns partial output into `IN` or `OUT`. Path-specific uncertainty stays local where possible.

## Known limits

- Only flat configuration is supported. ESLint 8 and legacy eslintrc are not modeled.
- Yarn Plug'n'Play and installs without a repository-contained `node_modules/eslint` package are unresolved.
- Nested ESLint 10 configuration lookup is unresolved in this slice.
- Discovery reads at most 100,000 repository entries. Reaching that limit makes repository-wide ESLint membership unresolved because later contexts may not have been discovered.
- Custom CLI arguments, explicit file lists, package scripts, Expo lint defaults, editor behavior, lint results, and fixes are outside this provider question.
- Directory queries are expanded by the core path source. A directory passed through as a literal provider path is `OUT` because ESLint eligibility applies to files.
- One context is evaluated in one batched Node process under the shared timeout and output bounds. Very large or slow executable configurations may therefore return `UNKNOWN`; splitting a context would reload repository code and could change its behavior.

## Sources

- [ESLint configuration files](https://eslint.org/docs/latest/use/configure/configuration-files)
- [ESLint Node.js API](https://eslint.org/docs/latest/integrate/nodejs-api)
- [ESLint ignore files](https://eslint.org/docs/latest/use/configure/ignore)
