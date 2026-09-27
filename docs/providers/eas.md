# EAS source archive provider

Provider ID: `eas`

Normative question:

> Would this repository path be present in the source archive uploaded by EAS Build for this project?

This is not membership in the final application artifact. Metro, native build tools, and remote build steps may use only part of the uploaded source archive.

## Current supported slice

The provider models the default Git workflow implemented by EAS CLI 24.3.0:

- the archive root is the Git worktree root;
- a Git-root `.easignore` replaces the repository's `.gitignore` hierarchy;
- without one, the current working tree is copied using the `.gitignore` hierarchy;
- `.git/info/exclude`, global excludes, and system/global Git configuration do not participate in that EAS copy filter;
- ignore matching is case-insensitive, following EAS CLI's `node-ignore` default;
- `.git` and `node_modules` are excluded by default;
- automatic project discovery skips hidden tool/cache directories and `node_modules`; use `--root` to inspect an intentionally hidden project directly;
- a nested `.easignore` is inactive and is reported as explanatory evidence;
- Git queries are fixed, read-only, batched, and executed only through the trusted-executable boundary;
- EAS CLI and repository JavaScript are never executed;
- `eas.json` is parsed as JSON5, matching the current EAS configuration reader.

`IN` means those archive rules include the current working-tree entry. `OUT` means the entry is absent or excluded by `.gitignore` or the default `node_modules` rule.

EAS archive membership is not Git tracking membership. EAS filters a copy of the working tree. A tracked file that matches the active archive ignore rules is `OUT` for EAS but `IN` for Git.

## Explicit uncertainty

The provider returns `UNKNOWN` rather than approximating when it finds:

- `cli.requireCommit: true`;
- `EAS_NO_VCS` mode;
- a path whose apparent inclusion depends on a nested `.gitignore` negation, because EAS documents cross-file behavior that differs from Git;
- an ignore/config file that cannot be inspected within the repository boundary;
- an incomplete repository scan, including the bounded discovery limit.

Malformed `eas.json` and native Git failures are operational/configuration errors, not `OUT` or `UNKNOWN`.

## Reference acceptance case

For an Expo app in `mobile/` under a larger Git repository, `mobile/.easignore` does not control the default EAS Git archive. Files excluded only by that nested ignore file remain `IN`. The result explains that `mobile/.easignore` is inactive.

## References

- [Expo `.easignore` guide](https://docs.expo.dev/build-reference/easignore/)
- [Current EAS Git archive implementation](https://github.com/expo/eas-cli/blob/main/packages/eas-cli/src/vcs/clients/git.ts)
- [Current EAS ignore implementation](https://github.com/expo/eas-cli/blob/main/packages/eas-cli/src/vcs/local.ts)
- [Current EAS JSON5 configuration reader](https://github.com/expo/eas-cli/blob/main/packages/eas-json/src/accessor.ts)
- [EAS CLI issue #4259: nested project `.easignore` is ignored](https://github.com/expo/eas-cli/issues/4259)
