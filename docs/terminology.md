# Terminology

This vocabulary is normative. Providers may add specific explanation codes and evidence, but they must not redefine these terms.

## Provider result

A result is one fact about one repository path from one provider instance.

- `IN`: the path belongs to the provider instance's effective scope.
- `OUT`: the path does not belong to the provider instance's effective scope.
- `N/A`: the provider instance does not meaningfully apply to the path.
- `UNKNOWN`: the provider applies, but `awareof` cannot establish the effective state safely and reliably.

`UNKNOWN` is a valid result, not an operational error. It prevents `awareof` from guessing when required information or an approved safe mechanism is unavailable.

Every result has an explanation:

- `code`: stable machine-readable identifier in `provider/detail` form;
- `summary`: short statement of why the state was returned;
- `evidence`: optional supporting fact;
- `action`: optional safe next step.

Every result also records its provenance. `tool` is a registered native entry-point ID. `reference` identifies a parser, library, or modeled specification:

- `safe-native`: established by an approved native executable through a provider-owned command plan;
- `safe-parser`: established without executing a provider tool, using a safe parser or reference implementation;
- `unavailable`: the provider could not establish the effective result with the required safe mechanism, including when that mechanism was unavailable, unapproved, or failed safely.

## Provider and instance

A provider represents one path-oriented system, such as Git or Docker. An instance is one independently evaluated scope within that system, such as one Docker build context or one npm package.

Provider and native-tool IDs are stable lowercase ASCII identifiers. They start with a letter and may contain digits and internal hyphens.

`detected` means that `awareof` found the provider or provider instance without executing repository-controlled code.

Provider-level presentation may collapse instances only when doing so preserves all effective states. `MIXED` is display text, not a provider state.

## Contract result

Contract status is separate from a provider state:

- `SATISFIED`: every applicable provider instance has the expected `IN` or `OUT` state;
- `MISMATCH`: at least one applicable instance has the opposite state, even if another instance is `UNKNOWN`;
- `UNRESOLVED`: no instance is a mismatch, but a required result is `UNKNOWN` or no applicable instance is available.

Status precedence is `MISMATCH` over `UNRESOLVED` over `SATISFIED`. All provider results remain available as details.

## Diagnostic

A diagnostic reports CLI, configuration, provider, or runtime behavior. Its level is `ERROR`, `WARNING`, or `INFO`. It uses the same `code`, `summary`, optional `evidence`, and optional `action` fields as an explanation.

An operational error means the command could not complete the requested operation. It exits with code `2`. An `UNKNOWN` result remains inspection data. During validation, it produces `UNRESOLVED` and exit code `1`.

## Native-tool terms

- **registered**: `awareof` contains a provider-owned definition for the tool and its fixed invocation plan.
- **selected**: a command name or explicit `--tool ID=PATH` identifies the entry-point candidate for this invocation.
- **available**: the selected candidate resolves to a regular entry-point file.
- **approved**: the user accepted the exact entry-point identity recorded by `awareof`.
- **executed**: an approved executable was started without a shell, using a provider-owned argument vector and hardened environment.

Selection is not approval. Detection is not execution. Approval is not a sandbox or an operating-system capability restriction. `awareof` uses an approval only for the provider's fixed operations. Repository configuration cannot supply arbitrary commands.
