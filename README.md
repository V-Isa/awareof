# awareof

See which tools include, exclude, or cover each repository file—and make CI catch unintended changes.

```text
$ awareof .env
.env
  docker           IN      included in Docker context; no matching exclusion
  git              OUT     ignored by .gitignore:1 pattern ".env"
  npm              OUT     not included in the npm publish tarball for example
```

Git will not track `.env`, but Docker will still receive it.

State what must remain true in `.awareof.yaml`:

```yaml
version: 1

rules:
  ".env*":
    git: out
    docker: out
    npm: out
```

```sh
awareof --validate
```

The workflow is: inspect effective behavior, find an unintended difference, assert the intended behavior, then protect it in development and CI.

If evaluation needs native tools, review and approve them first. Discovery does not execute them:

```sh
awareof --setup
```

Inspect files, directories, globs, staged paths, or changes from a revision:

```sh
awareof .env
awareof src/
awareof '**/*.generated.ts'
awareof --literal 'name[1].ts'
awareof --staged
awareof --changed-from main
```

## Status

`awareof` is under active development. It supports Git, repository-root Docker contexts, the default EAS Git archive, local CODEOWNERS coverage, npm publication tarballs, and effective TypeScript Program membership. It also supports staged and changed-from path selection and sparse validation contracts. Packaged binary distribution is not available yet.

Install the command from source with Go:

```sh
go install github.com/V-Isa/awareof/cmd/awareof@latest
```

This builds `awareof` locally from the published source. Prebuilt binaries and package-manager distribution are not available yet.

## Semantics

Providers return only:

```text
IN       path belongs to the provider's effective scope
OUT      path does not belong to the provider's effective scope
N/A      provider does not meaningfully apply to the path
UNKNOWN  effective state cannot be established safely and reliably
```

For Git, tracked paths are `IN` even if they also match an ignore rule. Untracked ignored paths are `OUT`. Untracked paths that are not ignored are `IN` because they are eligible for normal tracking.

For Docker, a repository-root `.dockerignore` defines the supported local context. Matching paths are `OUT`. Negated or unmatched paths are `IN`. `awareof` does not infer a context from a Dockerfile, run Docker, or discover Compose or Bake targets.

For EAS Build, the supported instance is the source archive rooted at the Git worktree. EAS Git mode reads `.easignore` only at the Git root, so `awareof` reports nested `.easignore` files as inactive. Unsupported EAS modes return `UNKNOWN`. `awareof` never runs EAS or repository JavaScript. See [the exact provider semantics](docs/providers/eas.md).

For CODEOWNERS, `IN` means the final valid matching rule declares one or more owners. `OUT` means no valid rule matches or the final rule has no owners. The provider reads the current worktree safely. It does not claim that GitHub verified the owners or will require a review. See [the exact provider semantics](docs/providers/codeowners.md).

For npm, `IN` means the path appears in npm's script-disabled dry-run tarball. An approved npm 11 or newer executable evaluates packages that have no pre-publication lifecycle script. `prepublishOnly`, `prepack`, or `prepare` makes paths in that package `UNKNOWN` because those scripts can change the artifact. `awareof` never runs them implicitly. Private packages and paths outside a workspace package are `N/A`. See [the exact provider semantics](docs/providers/npm.md).

For TypeScript, `IN` means at least one safely evaluated effective Program contains the path. `OUT` means every discovered Program resolved and none contains it. An unresolved compiler or project makes a path `UNKNOWN` unless another resolved Program already proves `IN`. TypeScript 6 and 7 require exact approved evaluators; `awareof` never runs `.bin/tsc` or repository-local TypeScript 6 JavaScript. See [the exact provider semantics](docs/providers/typescript.md).

## Path sources

Positional files, directories, globs, and stdin build a path set directly. Use `--literal` when a path contains glob characters such as `*`, `?`, or `[]`.

`--staged` selects paths changed in the Git index relative to `HEAD`. `--changed-from REF` selects tracked paths returned by `git diff REF` against the current worktree and index. It does not include untracked files. Renames select both the old and new path.

Git change selectors cannot be combined with each other, positional paths, or stdin. They only select paths. Every detected provider evaluates the result. Both selectors require an approved Git executable.

## Validation

`.awareof.yaml` is a sparse contract over effective provider states:

```yaml
version: 1

rules:
  ".env*":
    git: out
    docker: out

  "src/**":
    git: in
    codeowners: in
```

```sh
awareof --validate
awareof --validate src/
awareof --validate --json
awareof --validate -q
```

Exit code `0` means all matching assertions pass. Exit code `1` means at least one assertion is mismatched or unresolved, including an `UNKNOWN` result. Exit code `2` means a usage, configuration, provider, or runtime error. See [the exact contract semantics](docs/contracts.md).

## Safety

`awareof` must be safer to run against an unfamiliar repository than the native toolchain it inspects. It does not execute repository-controlled code implicitly. Native execution is an audited provider mechanism, not a default architecture.

Native tools require explicit approval before use:

```sh
awareof --setup
awareof .env
```

Setup shows each native entry point's canonical path, SHA-256 digest, origin, and trust status before approval. External tools can be approved together. Repository-controlled tools require separate approval for the current repository. Setup never runs a discovered tool. It does not prompt in a noninteractive environment.

Low-level, noninteractive trust controls remain available:

```sh
awareof --tools
awareof --approve-tool git
awareof --tool git=/absolute/path/to/git --approve-tool git
awareof --tool typescript=/absolute/path/to/lib/_tsc.js --setup
awareof --revoke-tool git
```

Approvals are stored in the platform user configuration directory, outside repositories. They are bound to the entry point's resolved path and SHA-256 digest. A repository-local entry point is also bound to that repository. A changed or unapproved required tool produces `UNKNOWN`; it is never run implicitly. See [native-tool execution](docs/tool-execution.md) and [the normative vocabulary](docs/terminology.md).

## Development

Use the Go version declared in `go.mod` or newer.

```sh
GOCACHE=$PWD/.cache/go-build go fmt ./...
GOCACHE=$PWD/.cache/go-build go test ./...
GOCACHE=$PWD/.cache/go-build go test -race ./...
GOCACHE=$PWD/.cache/go-build go vet ./...
GOCACHE=$PWD/.cache/go-build go build ./...
```

Unit tests use only Go and do not require provider tools on the host.

```sh
make integration
```

Integration and native-parity tests run inside a disposable Linux container. The command builds the versioned test image, then runs it without network access, host mounts, capabilities, or a writable root filesystem. The container and its fixture repositories are removed automatically; Docker may retain its normal image and build caches. The build may download the official Go and Node base images, Go modules declared in `go.sum`, and the exact TypeScript package versions declared in the integration Dockerfile. It does not install Node, npm, or TypeScript on the host.

Source publication does not include compiled dependency code. Before compiled artifacts are distributed, dependency licenses and required notices must be generated, verified, and included with them.

## License

MIT © 2026 Vadim Isaenko
