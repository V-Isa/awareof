# Security Policy

## Reporting a vulnerability

Report security issues privately through a GitHub security advisory after the repository is published. Until then, contact the maintainer directly.

Include the affected version, reproduction steps, impact, and sanitized diagnostic output. Do not include secrets, private repository contents, or personal data.

## Scope

Relevant issues include implicit execution of repository-controlled code, unsafe executable resolution, shell injection, path traversal, symlink escapes, configuration-driven execution, unintended network or daemon access, repository mutation, and sensitive path or content disclosure.

Native executables are never approved implicitly. Approval is bound to the selected entry point's resolved path and SHA-256 digest; repository-local approvals are also bound to the repository root. Approval is not publisher verification or a process sandbox. Providers remain responsible for fixed read-only command plans, environment hardening, and rejecting unsafe transitive behavior. See [native-tool execution](docs/tool-execution.md).

Before the first stable release, only the latest revision is supported.
