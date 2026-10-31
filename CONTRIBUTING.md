# Contributing to sys1

Thanks for being here. Every kind of help counts: bug reports, ideas, packages, translations, docs, testing on unusual machines, and code.

## The short version

1. **Open an issue** for anything: a bug, an idea, a package, a platform. Use the templates.
2. Talk it through there.
3. Work happens on **branches**. We don't use pull requests; changes land by pushing branches and merging them into `main` after discussion in the issue.

## How the repository works

| Branch | Purpose |
|---|---|
| `main` | Always releasable. CI must be green. Releases are tagged from here. |
| `develop` | Integration branch for the next release. |
| `feature/<short-name>` | One feature or fix, linked to its issue. |
| `docs/<short-name>` | Documentation-only work. |
| `release/vX.Y` | Stabilization for a release line, when needed. |

There are no pull requests. Instead:

- Maintainers push branches directly, named after their issue (`feature/42-pooled-storage`), and mention the branch in the issue.
- Outside contributors: attach a patch (`git format-patch`) or link a branch in your fork in the issue. A maintainer reviews it in the issue and brings it in, keeping your authorship.
- Review happens in the issue thread, so the whole history of a change lives in one place.

## Development setup

You need [Go](https://go.dev/dl/) (see `go.mod` for the version) and, optionally, Docker or Podman.

```bash
git clone https://github.com/A-Hackers-Guide/sys1
cd sys1
go test ./...             # unit and end-to-end tests
go test -race ./...       # the same, with the race detector
go run ./cmd/sys1         # start a node; the dashboard opens
go run ./cmd/sys0 join "$(go run ./cmd/sys1 join-code)" --no-service --data /tmp/c1
go run ./cmd/sys0 run --data /tmp/c1
```

## Standards

- **Tests** for every behaviour change. Reliability and security code needs tests that simulate failure (see `journal_test.go`, `node_test.go`).
- **Docs** in the same change. If behaviour changes, the docs change too.
- **Standard library first.** New dependencies need a good reason in the issue.
- **No CGO.** Everything must cross-compile with `CGO_ENABLED=0`.
- `gofmt`, `go vet` clean. CI checks both.
- Comments explain *why*, not *what*.
- The dashboard and wizard use plain JavaScript with no build step and no third-party code.
- User-facing text is plain and friendly. Error messages say what to do next.

## Commit messages

```
area: short summary in the imperative

Why the change is needed and anything non-obvious about it.

Refs #42
```

Areas: `agent`, `node`, `journal`, `watchdog`, `marketplace`, `protocol`, `gui`, `setup`, `docs`, `site`, `ci`, `packaging`.

## Reporting security issues

Please don't open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

## Respect

We respect you; please respect everyone here. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
