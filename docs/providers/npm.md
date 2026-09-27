# npm provider

## Question

For each detected package, the provider answers:

> Would this repository path be present in the tarball npm would publish for this package?

This is publication membership. It is not a report about one `.npmignore` or `package.json.files` rule.

## States

- `IN`: npm's safe dry-run output contains the path.
- `OUT`: npm's safe dry-run output does not contain the path.
- `N/A`: the path is outside this package, or the package has `private: true` and npm refuses to publish it.
- `UNKNOWN`: effective publication membership cannot be established safely.

Each root package and safely discovered workspace package is a separate instance. If the repository root has no `package.json`, `awareof` scans for nested package manifests. It skips `node_modules` and hidden metadata or tool-state directories. Use `--root` to inspect an intentionally hidden package. A nested or workspace instance applies only to paths below its package root.

## Safe native evaluation

The provider first checks the approved executable's version from `awareof`'s temporary directory. npm 11 or newer is required. It is the first major version where `npm pack --ignore-scripts` reliably covers the packing lifecycle. Older versions return `UNKNOWN` and do not pack the repository.

For a supported version, the provider uses fixed options equivalent to:

```text
npm pack PACKAGE --dry-run --json --ignore-scripts --offline
```

The provider does not use a shell. It runs from `awareof`'s temporary directory, so npm does not load the repository `.npmrc` as project configuration. It disables lifecycle scripts, npm extensions, Node preload variables, and network access. It uses temporary user configuration and cache paths outside the repository. It removes the temporary state after evaluation.

The npm entrypoint and its Node interpreter are resolved and approved separately. They use the same path-and-SHA-256 trust boundary as Git. `awareof` starts the approved Node binary directly, so `PATH` cannot select another interpreter. Both targets must be outside the inspected repository. Approval of one JavaScript entry file cannot cover all repository-controlled modules that npm can load. Detection reads manifests but runs neither tool.

## Publication scripts

`prepublishOnly`, `prepack`, and `prepare` run before npm creates the tarball. They can change any file. If one of these scripts is not empty, paths in that package are `UNKNOWN`. `awareof` does not run npm for that instance. `postpack` runs after tarball creation and does not cause this uncertainty.

## Workspaces

The provider discovers literal workspace paths and common patterns such as `packages/*`. A supported pattern contains only literal path segments and a whole-segment `*`. Both the array form and the `{ "packages": [...] }` form are supported.

npm implements its full workspace syntax with JavaScript minimatch and glob code. It is not identical to the `awareof` query language. Unsupported features include `**`, negation, character classes, braces, and extglobs. Such a declaration produces an unresolved workspace instance with `UNKNOWN`. `awareof` does not guess or run repository code.

## Limits

- Package manifests must be regular UTF-8 JSON files no larger than 1 MiB.
- Nested discovery stops after 100,000 repository entries. An incomplete scan is `UNKNOWN`; it does not prove that no package exists.
- A manifest that changes between detection and native evaluation produces `UNKNOWN`.
- Native npm evaluation returns `UNKNOWN` on Windows. The default `npm.cmd` launcher cannot be passed safely to the separately approved Node interpreter.
- A native command failure or malformed native output is an operational error, not an `OUT` result.
- The provider does not claim that registry authentication, naming, access, version uniqueness, or other remote publication requirements will succeed.

## References

- [npm publish](https://docs.npmjs.com/cli/v12/commands/npm-publish/)
- [npm pack](https://docs.npmjs.com/cli/v12/commands/npm-pack/)
- [npm lifecycle scripts](https://docs.npmjs.com/cli/v12/using-npm/scripts/)
- [npm workspaces](https://docs.npmjs.com/cli/v12/using-npm/workspaces/)
- [Official workspace mapping implementation](https://github.com/npm/map-workspaces)
