# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The release workflow refuses to publish a tag that has no section here, so a
release starts by writing one.

## [Unreleased]

### Repository

- Pull requests get one comment holding both comparisons, rewritten in place
  on each push: coverage before and after, then every benchmark before and
  after with its delta, green for an improvement and red for a regression.
- Coverage is compared against the base branch as well as against its floor.
  The floor says whether the module is tested well enough; it does not notice
  a change that sits comfortably above it while deleting half the tests for
  the code it touches. A drop of more than five points fails.
- Benchmark wall time is gated rather than only reported. Both sides are built
  with `-trimpath`, without which each carries its own build directory and the
  resulting shift in code and data can make identical source differ by several
  percent — a difference the comparison would otherwise blame on the change
  under review. The sides are run alternately so a runner drifting mid-job
  moves both together, and anything more than 5% slower is re-measured over a
  much longer window before it fails anything.
- The benchmark comparison checks the base ref out into a git worktree instead
  of over the top of the current one, so it no longer refuses to run with
  uncommitted changes and cannot leave the caller on the wrong commit.
- The benchmark run on `main` is a smoke run. There is nothing to compare
  against there, so measuring six times over bought nothing that each release
  then waited on.
- `shellcheck` runs over `tools/hack` as part of `tidy-check`.

## [0.1.0] - 2026-09-07

### Added

- Initial release: a Kubernetes `Broadcast` custom resource and a controller
  that fans one HTTP request out to every ready endpoint of a target Service,
  best-effort, with Prometheus metrics and a Helm chart.

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
  tree. Formatting is checked there and only there: it does not vary by
  operating system, and running it on a Windows checkout only finds line
  endings.
- `coverage-test` enforces a 35% floor, just under the current 36.9%. It is a
  ratchet, not a target: raise it as tests land.
- New workflows: CodeQL, OSV-Scanner, license scan against a permissive
  allowlist, Trivy image scan, OpenSSF Scorecard, and a stale-issue sweep.
- Release workflow: runs the suite, refuses a version that does not match
  `charts/broadcast/Chart.yaml` or has no section in this file, and publishes
  that section as the release notes. It is called by the CD stage and also
  runs on a hand-pushed `v*` tag.
- `golangci-lint` configuration pinned in `.golangci.yml`.
- Every GitHub Action is pinned to a commit SHA; dependabot proposes the
  bumps for actions, Go modules, and the Dockerfile base image.
- Releases are cut by merging to `main`: once every CI job passes there, the
  `tag` job tags the newest version in this file, if it is not tagged already,
  and the release job publishes it from that section.
