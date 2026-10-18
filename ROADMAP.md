# Roadmap

## Current vertical slice

- Canonical repository paths and path sets.
- Exact, directory, glob, and stdin queries.
- Git staged and changed-from path sources.
- Four-state provider contract.
- Git, Docker, EAS, CODEOWNERS, npm, Prettier, TypeScript, ESLint, and Jest providers.
- Sparse `.awareof.yaml` validation contracts.
- Human and JSON output.
- Explicit approval for native executables.
- Explicit invocation-scoped repo-code delegation for providers that require executable project configuration.
- Formal provider, contract, provenance, and diagnostic vocabulary.

## Next engineering slices

- Broader EAS modes and Compose/Bake Docker-context discovery.
- Git archive membership including `.gitattributes` `export-ignore`.
- Lossless directory and instance aggregation.
- Stable JSONL output.

## Ecosystem expansion

Research and dogfood additional providers only where the tool owns a meaningful repository-path scope:

- PHP: PHPUnit, PHPStan, and Composer.
- Java: Maven and Gradle.
- Go: effective Go package and build membership.
- GitHub Actions: static path filters and changed-set semantics.
- One AI provider only when effective file-context semantics are documented and safely readable.

Dogfood broadly. Promise narrowly.

Provider count never overrides correctness or safety. Unsupported effective behavior returns `UNKNOWN`. A provider is deferred when that would make it mostly uninformative.

## Public source

- Maintain the reviewed Go source and development repository with documented provider limits.

## Binary distribution later

- Release builds and package-manager distribution.
- Automated dependency license and NOTICE generation as a binary release gate.

## Out of scope

- Implicit execution of repository-controlled code.
- Persistent repository trust.
- Configuration editing or automatic fixes.
- Ignore-file synchronization.
- Runtime provider plugins.
- Generic repository command execution.
- Daemon, cloud service, account, or telemetry.
