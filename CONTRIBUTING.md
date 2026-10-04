# Contributing

`awareof` treats correctness and safe inspection as product behavior.

## Development

Use the Go version declared in `go.mod` or newer.

Run checks from the repository root:

```sh
GOCACHE=$PWD/.cache/go-build go fmt ./...
GOCACHE=$PWD/.cache/go-build go test ./...
GOCACHE=$PWD/.cache/go-build go test -race ./...
GOCACHE=$PWD/.cache/go-build go vet ./...
GOCACHE=$PWD/.cache/go-build go build ./...
GOCACHE=$PWD/.cache/go-build GOLANGCI_LINT_CACHE=$PWD/.cache/golangci-lint golangci-lint run ./...
GOCACHE=$PWD/.cache/go-build $(go env GOPATH)/bin/govulncheck ./...
```

Run `go mod tidy` after changing dependencies or imports.

Local unit tests require only Go. Do not run integration-tagged tests directly on the host. Use:

```sh
make integration
```

This runs integration tests and available native/reference parity tests in a restricted disposable container. The build may download the versioned official Go and Node base images, Go modules declared in `go.sum`, and the exact Prettier and TypeScript package versions declared in the integration Dockerfile. It does not install Node, npm, Prettier, or TypeScript on the host.

## Provider requirements

A provider must use only the normalized states `IN`, `OUT`, `N/A`, and `UNKNOWN`, and document which states apply to its semantics. It must discover instances without running repository code, identify authoritative semantics, and pass exhaustive behavioral tests. Where an authoritative executable or implementation exists, tests must verify native/reference parity. Otherwise, tests must use fixtures derived from authoritative documentation or reference sources. Ambiguity must produce `UNKNOWN`, not a guess. See [the normative vocabulary](docs/terminology.md).

Providers that need an executable must register a stable tool ID and use the shared execution boundary. Each provider owns its fixed arguments, environment hardening, timeout, output parser, and documented `UNKNOWN` cases. Repository configuration must never supply an arbitrary command. See [native-tool execution](docs/tool-execution.md).

## Code conventions

Use idiomatic Go and standard-library solutions where practical. Keep error messages lowercase, wrap inspectable causes, and use standard `got, want` test diagnostics. Keep provider-specific behavior outside core.

## Contributions and licensing

Contributions are licensed under the repository's MIT License. Submit only material you have the right to contribute.

AI-assisted contributions are allowed when the contributor has reviewed, understood, and tested them. Contributors remain responsible for submitted code and its licensing.
