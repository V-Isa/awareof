# Roadmap

## Current vertical slice

- Canonical repository paths and path sets.
- Exact, directory, glob, and stdin queries.
- Git staged and changed-from path sources.
- Four-state provider contract.
- Safe Git provider with explanations.
- Safe repository-root Docker-context provider using Docker's official matcher.
- Conservative EAS Git-workflow source-archive provider with inactive nested `.easignore` detection.
- Safe local CODEOWNERS coverage provider.
- npm publication provider with isolated, script-disabled native evaluation.
- Sparse `.awareof.yaml` validation contracts.
- Human and JSON output.
- Explicit path-and-digest approval for native executables.
- Formal provider, contract, provenance, and diagnostic vocabulary.

## Next engineering slices

- Broader EAS modes and Compose/Bake Docker-context discovery.
- Git archive provider for effective archive membership, including `.gitattributes` `export-ignore`.
- Separate Composer and Packagist dist-parity research because their archives have additional semantics.
- Lossless directory and instance aggregation.
- Stable JSONL output.

## Dogfooding and real-world validation

- TypeScript, Prettier, and ESLint when installed executables can be selected, explicitly approved, and used without unsafe repository-code execution.
- GitHub Actions with explicit static path-scope and changed-set semantics.
- One AI provider only when its effective file-context semantics are documented and safely readable.

These systems occur in repositories where `awareof` is being used and tested. They are real dogfooding directions, not hard prerequisites for the first public release. Dogfood broadly. Promise narrowly.

Provider count never overrides correctness or safety. Unsupported effective behavior returns `UNKNOWN`. A provider is deferred when that would make it mostly uninformative.

## Public source baseline

- Publish the reviewed Go source and development repository with Git, Docker, EAS, npm, and CODEOWNERS support.
- Do not require packaged binary distribution before the source repository becomes public.

## Binary distribution later

- Release builds and package-manager distribution.
- Automated dependency license and NOTICE generation as a binary release gate.

## Out of scope

- Executing repository-controlled code by default.
- Configuration editing or automatic fixes.
- Ignore-file synchronization.
- Runtime provider plugins.
- Daemon, cloud service, account, or telemetry.
