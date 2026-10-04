# Prettier provider

## Question

The provider answers one repository-level question:

> Does Prettier consider this path supported and nonignored in at least one detected Prettier project context?

This is file eligibility. It is not formatting compliance, whether formatting would change a file, or the path set selected by a package script.

## States

- `IN`: at least one safely evaluated context reports a parser and does not ignore the path.
- `OUT`: every applicable context resolves, and each context ignores the path or reports no parser.
- `UNKNOWN`: no context proves `IN`, and at least one applicable context cannot be evaluated safely.
- `N/A`: no detected Prettier context applies to the path.

Package contexts remain internal evidence. One repository-level result uses union semantics, so one resolved `IN` takes precedence over another unresolved or `OUT` context.

## Context discovery

A package directory is a context when its regular `package.json` declares Prettier directly in `dependencies`, `devDependencies`, or `optionalDependencies`. If no package context exists, a Prettier config or `.prettierignore` at the repository root creates a fallback context whose missing package remains `UNKNOWN`.

Discovery skips only `.git`, `.hg`, `.svn`, and `node_modules`. It does not treat every nested config as a separate invocation. An incomplete scan is unresolved rather than evidence that no context exists.

The context root models the Prettier invocation working directory. Configuration lookup can still walk through repository parents, as Prettier normally does. The snapshot preserves that repository-relative ancestry. Its default ignore files remain tied to the context root:

```text
<context>/.gitignore
<context>/.prettierignore
```

Nested `.prettierignore` files are not active unless their directory is another detected context. Installed Prettier packages are resolved safely from `node_modules/prettier` at the context or an ancestor within the repository. Symlinked package roots are accepted only when they resolve inside the repository.

Configuration lookup stops at the inspected repository boundary. `awareof` does not read or execute configuration from an ancestor outside that boundary, even though an unrestricted Prettier API call can search farther upward.

## Safe evaluation

The provider uses Prettier's public `getFileInfo` result as the native fact:

```text
ignored == false and inferredParser != null  -> IN
ignored == true                              -> OUT
ignored == false and inferredParser == null  -> OUT
```

It never runs `.bin/prettier`, `npx`, `npm exec`, a package script, or the project package. Evaluation requires:

- an approved external native Node executable;
- an approved external Prettier `index.mjs` entry point;
- an exact whole-package fingerprint match with the installed project package.

The provider supports stable Prettier 3.x. Native parity tests cover 3.0.0, 3.2.5, 3.3.0, 3.6.2, 3.8.5, and 3.9.9.

Select and approve the external API entry point once:

```sh
awareof --tool prettier=/opt/prettier/node_modules/prettier/index.mjs --setup
```

Later commands reuse an already approved evaluator when its exact package fingerprint matches the detected project package.

Before evaluation, `awareof` creates a private temporary snapshot. It mirrors only the context-root ignore files, applicable config files, repository-relative directory geometry, and contentless queried paths. This preserves config lookup and override matching without copying repository file contents. A passive root sentinel prevents config lookup from escaping the snapshot. The snapshot is removed after evaluation.

The evaluator first checks ignore state without resolving config. An ignored path can therefore resolve to `OUT` even when its config is unsafe. For a nonignored path, the evaluator finds the selected config and loads it only when `awareof` classified the mirrored file as passive.

Prettier's built-in ignores, including `node_modules` and version-control directories, still come from the exact evaluator. The context `.gitignore` and `.prettierignore` files are additional native inputs, not the complete ignore model.

Supported passive forms are:

- an object-valued `prettier` field in `package.json` or `package.yaml`;
- JSON or JSON5;
- YAML;
- TOML;
- an object-valued extensionless `.prettierrc`.

Configuration formats follow the exact evaluator version. `package.yaml` participates starting with Prettier 3.3. TypeScript configs participate starting with Prettier 3.5; when selected, they are executable and produce `UNKNOWN`. JavaScript configs are also executable and produce `UNKNOWN`. A string-valued shareable config produces `UNKNOWN`. If the effective passive config contains plugins, the path is `UNKNOWN`; the plugin is never imported. Passive `overrides` and parser overrides are resolved by the exact approved Prettier package.

## Exact package identity

The fingerprint covers every regular file in the Prettier package, using sorted package-relative paths, file sizes, and contents. Package-internal symlinks and special files are rejected. File count, individual size, and total size are bounded.

The project and evaluator fingerprints are compared before execution and recomputed afterward. Repository configs and ignore files copied into the snapshot are also rechecked. A patched, mismatched, incomplete, or changing package or config produces `UNKNOWN`.

This proves equality with the installed project package for this operation. It does not certify a publisher or make approval a sandbox.

## Normal `UNKNOWN` cases

The provider returns `UNKNOWN` when, for example:

- Prettier is declared but not installed in a supported `node_modules` layout;
- the installed version is not stable Prettier 3.x;
- Node or the external Prettier API entry point is missing, unapproved, or repository-controlled;
- a selected Node target is a script or version-manager shim rather than the direct native executable;
- package fingerprints differ or change;
- config discovery is incomplete;
- the effective config is executable, shareable, or uses plugins;
- a config is malformed, unreadable, or changes during evaluation;
- a queried symlink or line-break-containing path cannot be mirrored safely;
- the evaluator fails or returns invalid output.

Path-specific ambiguity remains local. Another safely evaluated context can still prove `IN`.

## Known limits

- Yarn Plug'n'Play and other installs without a discoverable `node_modules/prettier` package are unresolved.
- Package symlinks that resolve outside the repository are unresolved rather than inspected implicitly.
- Custom script working directories, `--ignore-path`, `--with-node-modules`, explicit plugin flags, and other invocation-specific options are outside this provider question.
- Configuration above the inspected repository root is deliberately not inherited.
- `.editorconfig` affects formatting options, not this eligibility result, and is not loaded.
- `requirePragma` and `checkIgnorePragma` are content-level formatting gates and are outside the `getFileInfo` question.
- Query targets are mirrored without contents because `getFileInfo` uses path identity. Native parity tests verify this behavior, including nested overrides and nonexistent paths.

## Sources

- [Prettier API](https://prettier.io/docs/api)
- [Prettier CLI ignore behavior](https://prettier.io/docs/cli#--ignore-path)
- [Prettier configuration files and overrides](https://prettier.io/docs/configuration)
- [Prettier ignore behavior](https://prettier.io/docs/ignore)
- [Prettier 3.3 package.yaml support](https://prettier.io/blog/2024/06/01/3.3.0.html)
- [Prettier 3.5 TypeScript config support](https://prettier.io/blog/2025/02/09/3.5.0)
