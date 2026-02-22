# gh-actions-versions

`gh-actions-versions` is a GitHub CLI extension that inspects workflows and
composite actions to ensure every external action reference is pinned to an
exact commit SHA that matches the intended release tag. It also helps keep
those pins up to date in bulk.

## Installation

```bash
gh extension install jclem/gh-actions-versions
```

The command above installs precompiled binaries when a tagged release exists.
If no release is available yet, GitHub CLI falls back to the source launcher
script in this repo, which builds the extension locally on first run and
requires `go` to be installed.

Local development can run the extension via `go run .` from the repository
root.

## Releasing

This repo publishes installable extension artifacts from tags that match `v*`.

```bash
git tag v0.1.0
git push origin v0.1.0
```

Pushing the tag triggers `.github/workflows/release.yml`, which runs
`cli/gh-extension-precompile` and attaches platform-specific binaries to the
GitHub release. After that, `gh extension install jclem/gh-actions-versions`
works without a local Go toolchain.

## Commands

| Command | Description |
| --- | --- |
| `gh actions-versions verify` | Validate that each `uses:` entry is pinned to a 40-char SHA and matches the tagged version comment. |
| `gh actions-versions fix` | Resolve tag comments to SHAs and rewrite the workflow to match (leaves untouched items that already align). |
| `gh actions-versions upgrade [owner/repo] [--version TAG]` | Re-pin every reference of an action to the latest release (or a specific tag). Use `--all` to upgrade every action. |
| `gh actions-versions update [owner/repo]` | Refresh commits using the existing version comment as the constraint (e.g., latest `v2.x`). Supports `--all`. |

Each command scans `.github/workflows/` and composite actions under
`.github/actions/`.

## Example

The `fix` command transforms unpinned action references into secure,
version-pinned SHAs:

**Before:**
```yaml
- uses: actions/checkout@v5
```

**After:**
```yaml
- uses: actions/checkout@08c6903cd8c0fde910a37f88322edcfb5dd907a8 # v5
```

The SHA corresponds to the commit for the specified version tag, and the
comment preserves the human-readable version for maintainability.

## Version Resolution

Version comments such as `# v2`, `# v2.1`, or `# v2.1.3` determine which
release stream to follow when pinning. The resolver walks releases (and then
tags) via the GitHub API, dereferencing annotated tags until it finds the
commit. Tags with major/minor specs always resolve to the newest matching
release.

## Development Workflow

```bash
go build ./...   # Compile the extension
go test  ./...   # Run unit tests with mocked GitHub API calls
go run   . fix   # Execute a command against the current repo
```

The repository relies on `gofmt` for code formatting—run it before committing.

## Testing

`main_test.go` contains comprehensive unit coverage for the resolver and
command flows. Tests use an in-memory mock REST client, so no real network
access is needed. Add new tests alongside features and ensure `go test ./...`
passes before opening a pull request.

## Contributing

When contributing:

- Use imperative, descriptive commit messages (e.g., “Add update command for
version specs”).
- Document behavioral changes in pull request descriptions and include sample
CLI output (`go test ./...`, `gh actions-versions verify`, etc.).
- Keep workflow examples in `testdata/sample/.github/workflows/` up to date to
demonstrate current expectations.

Refer to `AGENTS.md` for deeper contributor guidance. Contributions and issue
reports are welcome!
