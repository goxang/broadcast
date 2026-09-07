## What this changes

<!-- One or two sentences. Link the issue this closes, if any. -->

## Why

<!-- What was wrong or missing. For a bug, what the old behavior was. -->

## Checklist

- [ ] Tests cover the new behavior (and fail without the change)
- [ ] `make verify` passes
- [ ] `make helm-lint` passes if the chart or CRD changed
- [ ] `make e2e` passes if the controller or proxy behavior changed
- [ ] Chart version and `CHANGELOG.md` updated for a user-visible change
- [ ] Hot-path changes include before/after benchmarks below

## Benchmarks

<!-- Delete if not applicable. State CPU and Go version. -->
