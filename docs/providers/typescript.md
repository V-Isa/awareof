# TypeScript provider

The provider answers one repository-level question:

> Is this path part of at least one safely evaluated effective TypeScript Program in this repository?

It uses these states:

- `IN`: at least one resolved Program contains the path.
- `OUT`: every discovered Program resolved, and none contains the path.
- `UNKNOWN`: no Program conclusively contains the path, and at least one Program is unresolved.

`N/A` is not produced after the provider is detected. Project config paths are evidence, not public provider instances in this version.

## Discovery

`awareof` discovers repository-owned files named exactly `tsconfig.json`. It skips only `.git`, `.hg`, `.svn`, and `node_modules` directory trees. It does not guess that directories named `build`, `generated`, `vendor`, or similar are inactive projects. It then follows each valid `references` entry, including references to configs with other names. References cannot leave the repository.

An unreadable repository entry or a non-regular `tsconfig.json` makes discovery unresolved. Other readable projects are still evaluated, so a resolved Program can still prove `IN`.

`jsconfig.json` and unreferenced names such as `tsconfig.build.json` are not discovered automatically.

## Effective membership

The provider runs the exact approved compiler with fixed options equivalent to:

```sh
tsc -p CONFIG --listFilesOnly --pretty false
```

This asks TypeScript for the effective Program. The provider does not infer membership from `include` or `exclude`. Imports, `files`, `types`, triple-slash references, JavaScript enabled by `allowJs`, and JSON enabled by `resolveJsonModule` can all add files.

Before that operation, the same evaluator resolves the effective configuration with `--showConfig`. This is a safety preflight, not a membership oracle. `generateTrace` is rejected because TypeScript writes trace files even with `--listFilesOnly`. Node environment settings that can load code, change module identity, or write coverage, cache, warning, or diagnostic files are removed or disabled.

If the compiler exits unsuccessfully, the Program is unresolved. Partial stdout is discarded.

## Safe evaluators

The supported entry points are:

- TypeScript 6: an external `typescript/lib/_tsc.js`, run through a separately approved external Node executable;
- TypeScript 7: the platform package's native `lib/tsc` executable, which may be external or explicitly approved for the repository.

`awareof` does not run `.bin/tsc`, the TypeScript JavaScript launcher, `npx`, `npm exec`, package scripts, or repository-local TypeScript 6 JavaScript.

For TypeScript 7, `awareof` derives the native platform compiler from the project's installed package and shows it in `--setup`.

For TypeScript 6, `awareof` safely inspects an external official `tsc` installation on `PATH` when it resolves directly to the package's `bin/tsc`, then selects `lib/_tsc.js` only when the compiler-input fingerprint exactly matches the project. It does not parse platform command shims such as `tsc.cmd`. A previously approved matching external evaluator is also reused. PATH discovery reads files and metadata only. It never executes the launcher or compiler.

If no matching external TypeScript 6 installation is discoverable, select one during setup:

For example:

```sh
awareof --tool typescript=/opt/typescript/lib/_tsc.js --setup
awareof src/
```

TypeScript 6 also requires the approved `node` tool. The approval records the selected evaluator, so later commands can reuse it without repeating `--tool`.

## Exact package identity

Compiler behavior is version-sensitive. A matching version string is not enough.

For TypeScript 6, `awareof` fingerprints:

- `package.json` as stored;
- `lib/_tsc.js`;
- the complete named set and contents of `lib/lib*.d.ts`.

For TypeScript 7, it fingerprints the same package-relative inputs in the selected platform package, using native `lib/tsc` as the compiler core.

Paths are normalized and sorted. Each path and file content is length-delimited before hashing. Symlinks must resolve within the package tree. File count, individual size, and total size are bounded. The project and evaluator fingerprints are computed immediately before and after evaluation. A patched, incomplete, mismatched, or changing tree produces `UNKNOWN`.

The compiler core contains the Program construction and module-resolution logic. The standard-library declarations are compiler inputs that can become Program files, and `package.json` binds the package name and exact version. Hashing their complete named set and contents proves that the project and selected evaluator use the same bounded inputs relevant to this operation. It does not certify their publisher or make approval a sandbox.

## Normal `UNKNOWN` cases

The provider returns `UNKNOWN` when, for example:

- the exact project compiler installation cannot be identified;
- a TypeScript 7 platform package is absent from the supported adjacent install layout;
- the selected evaluator is missing, unapproved, unsupported, or a different version;
- TypeScript 6 is selected from inside the repository;
- package fingerprints differ or change during evaluation;
- the compiler fails or its line-oriented output is ambiguous;
- the effective config enables a write-causing compiler option such as `generateTrace`;
- a project reference is missing, malformed, or escapes the repository;
- a queried symlink or line-break-containing path cannot be mapped to TypeScript's line-oriented file list safely.

Path-mapping uncertainty is local to that queried path. It does not make unrelated paths `UNKNOWN`.

## Known limits

- `jsconfig.json` and unreferenced custom config names are not automatic discovery roots.
- TypeScript 7 native evaluation is supported only on Linux, macOS, and Windows, where the shared process-containment boundary is available.
- `--listFilesOnly` is line-oriented. A queried path that cannot be represented or mapped without ambiguity is `UNKNOWN`.

Configured language-service plugins are not loaded by the fixed compiler operation. Native parity tests include a plugin that would leave a marker if executed.

## Sources

- [TypeScript `listFilesOnly`](https://www.typescriptlang.org/tsconfig/listFilesOnly.html)
- [TypeScript `exclude`](https://www.typescriptlang.org/tsconfig/exclude.html)
- [TypeScript project references](https://www.typescriptlang.org/docs/handbook/project-references)
- [TypeScript 7.0 announcement](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/)
