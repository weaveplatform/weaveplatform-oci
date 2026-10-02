# 0013: Quality gates: coverage and acceptance per phase

Status: Proposed

## Context

The project owner requires a quality-gate pipeline with 95% test coverage, and
acceptance tests delivered as part of every implementation phase. The implementation
is Go; helper scripts may be in other languages.

Much of the module's behaviour depends on external systems:
- registries with and without the referrers API;
- multi-GiB transfers that are interrupted and resumed;
- signatures and channel manifests;
- operating-system specifics such as sparse files and hole punching.

hostweave already runs this kind of gate (decision 0018: ≥95% total, a 90% per-package
floor, godog acceptance tests against real binaries, testcontainers-go), so the
organisation has one way of doing it.

## Decision

**Every pull request** must pass all of the following on Linux, and the
platform-specific suites on macOS and Windows runners. The workflow is
`.github/workflows/quality-gate.yml`; its `Quality gate` job is the single required
status check. The OS matrix always uses the latest runner labels (`ubuntu-latest`,
`macos-latest`, `windows-latest`), never pinned versions:

| Gate | Tool | Threshold |
|---|---|---|
| Build and vet | `go build ./...`, `go vet ./...` with `GOWORK=off` | no errors |
| Lint | golangci-lint with the repository's `.golangci.yml` | no findings |
| Unit and contract tests | `go test -race -shuffle=on -count=1` | all pass |
| Acceptance tests | godog features under `test/acceptance/features/` | all pass |
| Coverage | merged unit plus acceptance coverage, enforced by `vladopajic/go-test-coverage` (or an equivalent script) | **≥95% total**, ≥90% per package |
| Vulnerabilities | `govulncheck ./...` | no reachable findings |
| Artifact contract | the conformance suite over `pkg/spec/testdata/` | all fixtures pass; negative fixtures fail |

**Unit tests.**
- Use stdlib `testing` and `github.com/google/go-cmp/cmp`.
- Table-driven, with hand-written fakes behind narrow interfaces such as `Clock`,
  `HolePuncher` and `DiskSpaceGuard`.
- Registries are in-process: oras-go `memory` and `oci` stores, plus an `httptest`
  registry. The `httptest` registry can be configured to omit the referrers API, refuse
  Range, drop connections mid-blob and return 5xx, so every client branch is
  reachable.

**Acceptance tests.**
- Use godog (Gherkin).
- Steps build the real `weaveoci` binary with `go build -cover` and set `GOCOVERDIR`
  per run, so acceptance coverage counts toward the gate.
- They run against real registries started with testcontainers-go v0.44.0:
  - `weave-zot` in the store role and in the mirror role, built from `deploy/zot` in
    the same pipeline;
  - `registry:3.1.2` for the fallback-tag path.
- Readiness is a wait on `/readyz` for zot and on `/v2/` for distribution.

**Optional and nightly suites.** A GHCR smoke test (push, pull and verify against a
scratch package) runs nightly and on demand, and does not count toward the gate. The
same applies to hypervisor runs in the consumer repositories.

**Coverage merging.**
- Unit coverage goes to `cover/unit`, acceptance coverage to `cover/accept`, and
  macOS/Windows runner coverage to `cover/<os>`.
- `go tool covdata merge` combines them and `textfmt` produces the profile.
- Platform-specific files carry build tags and are covered on the runner that compiles
  them.
- Generated code and `cmd/` `main` shims are excluded.

**Acceptance tests are part of each phase.** Every phase in
[11-migration.md](../11-migration.md) lists the feature files it delivers. A phase is
not done until those features pass in CI and coverage holds at the gate. A feature for
behaviour not yet built is tagged `@pending` and is excluded from the run; nothing
`@pending` remains when its phase closes.

**Fixtures are normative.** A change to the contract and its fixtures land in one
commit. The conformance suite is exported so that hostweave and both guestweave CLIs
run the same checks.

**Helper scripts.** CI glue and the coverage-merge helper may be shell or Python under
`scripts/`. Product logic lives in Go and is covered by the gate.

**Local parity.** `make test`, `make accept`, `make cover`, `make lint` and
`make vuln` run the same commands as CI.

## Rationale

Matching hostweave's decision 0018 gives the organisation one testing method. The same
thresholds and tools apply, and reviewers already know how to read them.

Merging acceptance coverage means the 95% figure measures behaviour exercised through
the real binary against real registries, not just unit tests against fakes.

Real zot and distribution containers catch protocol differences that an in-memory
registry hides. The ones that matter here are:
- zot's 60 s default timeout;
- zot's non-validation of layers for custom config types;
- distribution's missing referrers API.

Tying acceptance features to phases stops a phase being declared done on unit tests
alone.

Alternatives considered:
- **95% per package.** Rejected because small OS-specific glue packages (hole punching
  on APFS or NTFS, keychain hooks) cannot reach it on every runner. A 90% floor keeps
  them honest, and the 95% total keeps the whole.
- **Acceptance tests in plain Go without Gherkin.** Rejected to stay consistent with
  hostweave, and because feature files read as the acceptance criteria of each phase.
- **Coverage from unit tests only.** Rejected: it rewards testing fakes rather than the
  binaries operators run.
- **GHCR in the per-PR gate.** Rejected: it needs credentials, is rate-limited and is
  nondeterministic. It runs as a nightly smoke test instead.

## Constraints

- testcontainers-go needs Docker. Linux CI always has it. On macOS and Windows runners
  without Docker, registry acceptance features are skipped with a logged reason, and
  their coverage comes from the Linux run.
- Multi-GiB transfer behaviour is tested at reduced chunk sizes in the gate. A full-size
  transfer (one 512 MiB chunk, and a resumable multi-GiB pull) runs in the nightly
  suite.
- Coverage of verification against live Sigstore infrastructure uses recorded bundles
  and a pinned trusted root. Live verification is nightly.
- Every new package must meet the floor before it merges.

## Verification

- `make cover` fails below 95% total or below 90% for any package. CI uploads the
  merged profile.
- A deliberately uncovered test package in the gate's own self-test proves the
  threshold is enforced.
- The CI workflow `ci.yml` shows the gates as required status checks on `main`.
- Each phase's pull request references its feature files. The phase table in
  [11-migration.md](../11-migration.md) links them.

## References

- hostweave testing decision: `weaveplatform/hostweave@main docs/research/decisions/0018-testing-and-coverage.md`
- [10-shared-go-module.md](../10-shared-go-module.md), [11-migration.md](../11-migration.md), [13-deployment-profiles.md](../13-deployment-profiles.md)
- godog: <https://github.com/cucumber/godog>
- testcontainers-go v0.44.0: <https://github.com/testcontainers/testcontainers-go/tree/v0.44.0>
- testcontainers-go registry module: <https://github.com/testcontainers/testcontainers-go/tree/v0.44.0/modules/registry>
- go-test-coverage: <https://github.com/vladopajic/go-test-coverage>
- Go coverage for integration tests: <https://go.dev/doc/build-cover>
- govulncheck: <https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck>
