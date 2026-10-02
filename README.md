# weaveplatform-oci

One OCI image approach for the weave projects: the **weave guest artifact
contract** for macOS, Windows and Linux VM images, a **Go module** that packs,
inspects, transfers and verifies those artifacts, the **`weaveoci` CLI**, the
**`weave-zot`** reference private registry, and the publication workflows used
by hostweave, guestweave-cli-macos and guestweave-cli-windows.

Start with the [research and design set](docs/research/README.md): the contract
is [09-artifact-contract-v1.md](docs/research/09-artifact-contract-v1.md), the
module design is [10-shared-go-module.md](docs/research/10-shared-go-module.md),
and the phases are in [11-migration.md](docs/research/11-migration.md).

## Status

Phase 1 is implemented: the public packages `pkg/spec`, `pkg/chunk`,
`pkg/pack` and `pkg/conformance`, `weaveoci pack|inspect|unpack|healthcheck`,
the `weave-zot` image, and the quality gate.

## Layout

| Path | Contents |
|---|---|
| `pkg/` | Public packages imported by hostweave and the guestweave CLIs |
| `internal/` | Packages used only by this project (CLI commands, build info, test bundle generator) |
| `cmd/weaveoci/` | The CLI entry point |
| `deploy/zot/` | The `weave-zot` image, config roles and Compose file |
| `test/acceptance/` | godog features run against the real binary and registries |
| `docs/research/` | Research, contract, architecture and decision records |
Registry transport, caching, signing and publishing arrive in phase 2.

## Quick start

```sh
make build                                   # bin/weaveoci-<os>-<arch>
weaveoci pack ./bundle --out ./layout --tag 26.0-25A354-r1
weaveoci inspect ./layout --strict --deep    # contract conformance checklist
weaveoci unpack ./layout ./out --resume      # sparse raw disks + state files
```

A bundle is a directory of raw disks, state files and a `bundle.json`
([format](docs/research/10-shared-go-module.md#71-bundle-directory-format)).

## Quality gate

| Command | What it runs |
|---|---|
| `make test` | unit tests with `-race -shuffle=on`, coverage to `cover/unit` |
| `make accept` | godog features against the real `weaveoci` binary, `weave-zot` and `registry:3.1.2` in Docker |
| `make cover` | merged coverage: **≥95% total, ≥90% per package** |
| `make lint` / `make vuln` | golangci-lint and govulncheck |
| `make gate` | all of the above, as CI runs them ([quality-gate.yml](.github/workflows/quality-gate.yml)) |

The module builds with `GOWORK=off` (the Makefile sets it).

## Private registry

```sh
cd deploy/zot && htpasswd -bBn publisher '<password>' > htpasswd && docker compose up -d
```

See [deploy/zot](deploy/zot/README.md) and
[deployment profiles](docs/research/13-deployment-profiles.md).

## License

MIT, see [LICENSE](LICENSE).
