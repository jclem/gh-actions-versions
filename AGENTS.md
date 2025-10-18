# Repository Guidelines

## Project Structure & Module Organization
- Core extension code lives in `main.go`; supporting tests reside in `main_test.go`.
- Sample workflows for manual smoke tests sit under `testdata/sample/.github/workflows/`.
- Go module metadata is managed via `go.mod` and `go.sum`; GitHub workflows reside in `.github/workflows/`.

## Build, Test, and Development Commands
- `go build ./...` — compile the CLI extension and verify dependencies resolve.
- `go test ./...` — run the full unit test suite, including resolver and command-path coverage.
- `go run . <command>` — execute the extension locally (e.g., `go run . verify`).

## Coding Style & Naming Conventions
- Go files follow standard formatting enforced by `gofmt`; run `gofmt -w <files>` before submitting changes.
- Maintain 2-space indentation in YAML workflows and preserve existing comment markers such as `# v5`.
- Exported identifiers use CamelCase; locals prefer short, descriptive names aligned with Go idioms.

## Testing Guidelines
- Tests use Go’s built-in `testing` package with mock REST clients; keep new tests in `main_test.go`.
- Name tests using `Test<Feature>` and include focused subtests where flows diverge.
- Ensure `go test ./...` passes without network access by mocking GitHub API interactions.

## Commit & Pull Request Guidelines
- Craft commits with concise, imperative summaries (e.g., “Add update command for version specs”).
- Pull requests should describe the problem, highlight key code paths touched, and reference related issues or workflows.
- Include CLI output snippets (e.g., `go test ./...`) when they demonstrate behavior changes.
