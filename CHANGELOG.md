# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The release workflow refuses to publish a tag that has no section here, so a
release starts by writing one.

## [Unreleased]

### Changed

- The Broadcast informer uses `ListWithContextFunc` and `WatchFuncWithContext`
  instead of the deprecated `ListFunc` and `WatchFunc`, so the informer's
  context reaches the API calls rather than a captured one.
- `conditionEqual` takes its wanted condition by pointer; `metav1.Condition`
  is 96 bytes and was being copied on every status reconcile.

### Repository

- Make targets covering every check CI runs, so a red build reproduces
  locally.
- CI split into named jobs: `lint`, `tidy-check`, `test`, `coverage-test`,
  `go-benchmark-test`, `vuln`, `helm`, `docker`, and `e2e`.
- `go-benchmark-test` compares a pull request's benchmarks against the base
  branch. Allocation counts are deterministic, so an increase fails the job;
  wall-clock deltas are reported but never gated.
- `tidy-check` fails if `go mod tidy` or `gofmt` would change the committed
  tree.
- New workflows: CodeQL, OSV-Scanner, license scan against a permissive
  allowlist, Trivy image scan, OpenSSF Scorecard, and a stale-issue sweep.
- Release workflow on `v*` tags: runs the suite, refuses a tag whose version
  does not match `charts/broadcast/Chart.yaml` or has no section in this file,
  and publishes that section as the release notes.
- `golangci-lint` configuration pinned in `.golangci.yml`.
- Every GitHub Action is pinned to a commit SHA; dependabot proposes the
  bumps for actions, Go modules, and the Dockerfile base image.

## [0.1.0]

### Added

- Initial release: a Kubernetes `Broadcast` custom resource and a controller
  that fans one HTTP request out to every ready endpoint of a target Service,
  best-effort, with Prometheus metrics and a Helm chart.
