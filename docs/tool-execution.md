# Native-tool execution

`awareof` uses native tools only when they provide effective truth that a safe parser cannot establish as reliably. A provider may also use no executable at all.

## Trust boundary

The provider owns the tool ID, entry-point form, arguments, input format, environment changes, timeout, output limit, and result parser. The user selects and approves an entry-point file. If a tool needs an interpreter, the provider selects it and requires separate approval. A shebang or `PATH` cannot select it implicitly. Repository configuration cannot supply arbitrary commands. `awareof` never uses a shell for provider evaluation.

Discovery does not execute tools. Before execution, `awareof`:

1. resolves symlinks to the entry point's physical path;
2. requires a regular entry-point file;
3. calculates its SHA-256 digest;
4. classifies it as external or inside the inspected repository;
5. checks explicit approval for that exact identity;
6. removes dangerous loader variables and provider-specific environment variables;
7. contains ordinary descendants in a dedicated process group on Linux and macOS, or a Job Object on Windows;
8. applies a timeout and bounded output capture.

Cancellation, timeout, and normal command return all stop remaining contained descendants. Native execution fails closed on platforms where `awareof` does not implement process-tree containment.

If the tool is missing, changed, or not approved, the provider returns `UNKNOWN` with an action. It does not run the tool and does not guess.

Approval is a user trust decision. It is not permission enforced by the operating system. `awareof` uses an approval only for the provider's fixed operations. A digest identifies the selected entry-point file and invalidates approval when its bytes change. It does not prove that the executable is safe or comes from a specific publisher. It also does not identify every interpreter, library, plugin, or configuration file that the executable can load. The provider must assess these behaviors. It returns `UNKNOWN` when its fixed invocation cannot meet the safety contract.

This mechanism is an execution gate. It is not a sandbox against a malicious process that runs as the same operating-system user. On Unix, a malicious process can deliberately leave its process group. Providers must still use the least powerful safe mechanism. Approval does not make every tool operation safe.

## Approval storage

Approvals are stored outside repositories in the platform user configuration directory:

- macOS: `~/Library/Application Support/awareof/tool-approvals.json`
- Linux: `$XDG_CONFIG_HOME/awareof/tool-approvals.json`, or `~/.config/awareof/tool-approvals.json`
- Windows: `%AppData%\awareof\tool-approvals.json`

The file is versioned JSON and limited to 1 MiB. Approval changes use an operating-system lock on a stable sibling file. This keeps concurrent `awareof` processes from overwriting each other's changes. The lock file remains on disk; its presence does not mean that a process holds the lock. A temporary file in the same directory replaces the approval file. The file is private where platform permissions support that guarantee. Replacement is atomic when the operating system permits a rename over the existing file. On Windows, the fallback removes the old file immediately before the rename. The approval file cannot be inside the inspected repository, including through a symlinked parent directory.

Each approval record contains:

- tool ID;
- resolved entry-point path;
- SHA-256 digest;
- entry-point origin;
- repository root when the entry point is inside that repository.

An external entry-point approval applies across repositories only while its path and digest remain unchanged. A repository-local entry-point approval applies only to the repository in which it was approved. `--revoke-tool ID` removes all recorded approvals for that tool ID.

## Commands

```sh
awareof --setup
awareof --tools
awareof --approve-tool git
awareof --tool git=/absolute/path/to/git --approve-tool git
awareof --revoke-tool git
```

`--approve-tool` and `--revoke-tool` may be repeated for batch operations. `--tool ID=PATH` selects an entry point only for the current invocation; it never approves it implicitly.

`--setup` determines which registered tools the detected native-backed providers can use, then discovers only those tools. It does not run them. It shows each path, digest, origin, and trust status. In a terminal, one confirmation can approve all unapproved external tools. Repository-controlled tools need separate approval and remain repository-scoped. Before approval, `awareof` resolves and hashes the displayed identity again. A change stops approval. Without an interactive terminal, setup reports status but does not prompt or change approvals.

The low-level commands are explicit and noninteractive. They support deterministic CI setup. Normal inspection never pauses for a prompt.

## Current native use

Git inspection, Git change path sources, and the current EAS Git-workflow model use the registered `git` executable. They share one approval. Each use has fixed read-only operations and a sanitized Git environment.

npm publication inspection uses separately approved `npm` and `node` targets. Both targets must be outside the inspected repository. The npm entrypoint loads JavaScript modules that its file digest does not cover. The provider runs that entrypoint through the approved Node executable and isolated temporary configuration. It disables network access and lifecycle scripts. It does not use npm versions older than 11 for packing.

TypeScript Program inspection uses a safely derived or explicitly selected evaluator. TypeScript 6 uses an external `lib/_tsc.js` through separately approved external Node. TypeScript 7 uses the platform package's native `lib/tsc`. The provider compares bounded compiler-input fingerprints before and after evaluation. It does not run `.bin/tsc`, npm commands, package scripts, or repository-local TypeScript 6 JavaScript. See [the TypeScript provider contract](providers/typescript.md).

Docker context membership uses Docker's official Go matcher. CODEOWNERS uses a safe parser. Neither executes a native tool.
