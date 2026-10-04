# Docker provider

Provider ID: `docker`

## Question answered

For the currently supported repository-root local context:

> Does this repository path belong to the Docker build context after effective `.dockerignore` handling?

This is context membership. It does not claim that a Dockerfile copies the path into an image or that the path survives any image stage.

## States

- `IN`: no exclusion matches, or the final matching rule is a negation that includes the path.
- `OUT`: the final effective rule excludes the path.
- `N/A`: not produced by the current repository-root context instance.
- `UNKNOWN`: the root `.dockerignore` exists but cannot be read safely, so effective membership cannot be established.

The context root and `.dockerignore` itself are `IN`. Docker sends `.dockerignore` as context metadata even when a pattern names it.

## Discovery and matching

The provider detects one instance only when `.dockerignore` exists at the repository root. That file explicitly establishes the supported context as the repository root. A Dockerfile alone does not establish its build context, so it does not activate this provider.

The provider reads `.dockerignore` with Docker's official ignore-file parser and evaluates it with [`moby/patternmatcher`](https://github.com/moby/patternmatcher). This preserves Docker behavior for preprocessing, `*`, `?`, character classes, recursive `**`, negation with `!`, parent-directory matching, and last-match precedence.

Matching uses paths relative to the context root. Existing files, nonexistent logical paths, and symlink entries are matched by path name; evaluation does not traverse a queried symlink outside the repository.

## Safety and limits

The provider is a safe parser. It does not run Docker, contact a daemon, build an image, execute a Dockerfile, or make a network request. It requires no native-tool approval.

The `.dockerignore` file must be a stable regular file no larger than 4 MiB. A symlink, oversized file, read failure, or replacement while the file is opened makes ordinary paths `UNKNOWN`. Invalid pattern syntax is a configuration error rather than an `OUT` result.

The current provider does not discover Compose or Bake targets, named contexts, remote contexts, nested contexts, Dockerfile-specific ignore files, or build invocations. It does not select a Dockerfile, so it also does not model Docker's special metadata treatment for the selected Dockerfile. These cases require an explicit, trustworthy context and Dockerfile association rather than guessing from file locations.

## Examples

Given:

```text
# .dockerignore
.env
*.log
!keep.log
```

the provider reports:

```text
.env       docker  OUT  excluded from Docker context by .dockerignore pattern ".env"
debug.log  docker  OUT  excluded from Docker context by .dockerignore pattern "*.log"
keep.log   docker  IN   included in Docker context by .dockerignore pattern "!keep.log"
app.go     docker  IN   included in Docker context; no matching exclusion
```

## Validation strategy

Decision tables cover root-context detection, official ignore-file normalization, wildcard forms, negation, last-match behavior, context metadata, unreadable input, symlink escape attempts, size bounds, invalid patterns, and cancellation. The matcher is Docker's official Go implementation, so no Docker daemon is required for parity.

## Sources

- [Docker: Build context](https://docs.docker.com/build/concepts/context/)
- [Docker: `moby/patternmatcher`](https://github.com/moby/patternmatcher)
