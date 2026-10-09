# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.4](https://github.com/weaveplatform/weaveplatform-oci/compare/v0.1.3...v0.1.4) (2026-10-09)


### Features

* authenticate image candidates separately from channel admission ([#46](https://github.com/weaveplatform/weaveplatform-oci/issues/46)) ([acbfaa6](https://github.com/weaveplatform/weaveplatform-oci/commit/acbfaa685f89813c9314ff010d1afbc84a8e62a1))
* validate prepared macOS image handoff and acceptance ([#50](https://github.com/weaveplatform/weaveplatform-oci/issues/50)) ([9aee864](https://github.com/weaveplatform/weaveplatform-oci/commit/9aee8641bd6ea21af82c6f3f3f5a4f3aa8d360a8))


### Bug Fixes

* **publish:** preserve guest versions and reviewed signer metadata ([#48](https://github.com/weaveplatform/weaveplatform-oci/issues/48)) ([cbcbfb6](https://github.com/weaveplatform/weaveplatform-oci/commit/cbcbfb6ad311eb00b05ac48bdaabb03d7494026f))

## [0.1.3](https://github.com/weaveplatform/weaveplatform-oci/compare/v0.1.2...v0.1.3) (2026-10-08)


### Features

* **images:** import Imageweave candidates and authenticate image delivery ([#42](https://github.com/weaveplatform/weaveplatform-oci/issues/42)) ([2c44f6e](https://github.com/weaveplatform/weaveplatform-oci/commit/2c44f6e62f679a7fff9a21066e11f3e2b895ec1f))
* **images:** validated publication and target delivery ([#40](https://github.com/weaveplatform/weaveplatform-oci/issues/40)) ([217c4ba](https://github.com/weaveplatform/weaveplatform-oci/commit/217c4ba38611f9f881e8d86b45fbbde1fcb06488))
* **images:** verify published image admission evidence ([#44](https://github.com/weaveplatform/weaveplatform-oci/issues/44)) ([5f8bebb](https://github.com/weaveplatform/weaveplatform-oci/commit/5f8bebbcdf6f3e6534b8ccf79e592512791bc01c))

## [0.1.2](https://github.com/weaveplatform/weaveplatform-oci/compare/v0.1.1...v0.1.2) (2026-10-07)


### Features

* **images:** add native image builders and source controls ([#36](https://github.com/weaveplatform/weaveplatform-oci/issues/36)) ([c812fdb](https://github.com/weaveplatform/weaveplatform-oci/commit/c812fdb2ab40000223d3c14f8052d953c82349cb))

## [0.1.1](https://github.com/weaveplatform/weaveplatform-oci/compare/v0.1.0...v0.1.1) (2026-10-04)


### Features

* base and derived image tiers with recorded lineage ([fd9239f](https://github.com/weaveplatform/weaveplatform-oci/commit/fd9239f4b10c417706857943a9fcc4e92e16de50))
* base and derived image tiers with recorded lineage ([e5341d6](https://github.com/weaveplatform/weaveplatform-oci/commit/e5341d6d738e4e3f8b4ec4d153d83fcba9fcaea0))
* **fetch:** one public consumer path for pull, verify, pin and unpack ([05809de](https://github.com/weaveplatform/weaveplatform-oci/commit/05809de43d173e502180df0ca9593a7b56a14834))
* **fetch:** one public consumer path for pull, verify, pin and unpack ([f3e2398](https://github.com/weaveplatform/weaveplatform-oci/commit/f3e23985ea09964b1ad4e7d7a5f280cb2a3203a0))
* Linux cloud-image pipeline (ubuntu-24.04) ([27d7bfa](https://github.com/weaveplatform/weaveplatform-oci/commit/27d7bfa518d1995034814165a072ad3f7208f3f7))
* shared-module prerequisites for the guestweave refactor ([1cd89a2](https://github.com/weaveplatform/weaveplatform-oci/commit/1cd89a23d68fa309f6f78296eece18d0e0d772dc))
* shared-module prerequisites for the guestweave refactor ([62e6407](https://github.com/weaveplatform/weaveplatform-oci/commit/62e6407855b9ee27a14e55966b6421d96f3c006e))


### Bug Fixes

* **images:** read the trusted root from a file, not a pipe into head ([f2fc605](https://github.com/weaveplatform/weaveplatform-oci/commit/f2fc605d81dac9a3df28c8393a5597f7a797b28f))
* **images:** read the trusted root from a file, not a pipe into head ([b76e8e5](https://github.com/weaveplatform/weaveplatform-oci/commit/b76e8e59bfcf533f62a70e5a997f0708e87e801e))
* **images:** validate the profile file by path in the step that writes it ([a017435](https://github.com/weaveplatform/weaveplatform-oci/commit/a017435e3384e6ec524da963cdf0598a71ee178c))
* **images:** validate the profile file by path in the step that writes it ([d21ccb9](https://github.com/weaveplatform/weaveplatform-oci/commit/d21ccb914daa3a4172d10ced1cfa4e29b86a55ee))
* **verify:** say an artifact carries no signatures instead of printing %!w(&lt;nil&gt;) ([22cc937](https://github.com/weaveplatform/weaveplatform-oci/commit/22cc9378de73ac410c93c003b98190083c5e1418))
* **verify:** say an artifact carries no signatures instead of printing %!w(&lt;nil&gt;) ([7a562cb](https://github.com/weaveplatform/weaveplatform-oci/commit/7a562cb453b41690f994fac645b86028c420708c))

## 0.1.0 (2026-10-02)


### ⚠ BREAKING CHANGES

* import path and annotation keys changed.

### Features

* **channel:** validate channel output against agent-core's schema ([f09110f](https://github.com/weaveplatform/weaveplatform-oci/commit/f09110f0e408a9739503ba8534acac75d2c36c53))
* **channel:** validate channel output against agent-core's schema ([e877072](https://github.com/weaveplatform/weaveplatform-oci/commit/e877072c94d46a9ef74fcf1e32b4bcaaaec3a272))
* guest artifact contract, registry client, signing and quality gate ([e2119e1](https://github.com/weaveplatform/weaveplatform-oci/commit/e2119e127d972fccb63710430ffcb0a75a45303f))
* guest artifact contract, registry client, signing and quality gate ([f3fb7aa](https://github.com/weaveplatform/weaveplatform-oci/commit/f3fb7aa1b4b5aecb8face2cfb5e1c6d721e262ee))
* publish the weaveoci and weave-zot images ([5b4f7d2](https://github.com/weaveplatform/weaveplatform-oci/commit/5b4f7d2870497bd1ab469d6589bdf070d9b2dbeb))
* publish the weaveoci and weave-zot images ([af763b9](https://github.com/weaveplatform/weaveplatform-oci/commit/af763b9cda647fad81230a1b53a5c9930b584006))


### Bug Fixes

* **ci:** Windows-correct tests, gofmt, Super-Linter Go validators off ([a0c96b3](https://github.com/weaveplatform/weaveplatform-oci/commit/a0c96b37a6214bc632111895ccd7598904e15f4a))


### Code Refactoring

* move to the weaveplatform organisation ([6d2c323](https://github.com/weaveplatform/weaveplatform-oci/commit/6d2c323d3d3d13d3d096b533940eeb2ca2102c92))

## [Unreleased]

### Added

- Added xyz [@your_username](https://github.com/your_username)

### Fixed

- Fixed zyx [@your_username](https://github.com/your_username)

## [1.1.0] - 2021-06-23

### Added

- Added x [@your_username](https://github.com/your_username)

### Changed

- Changed y [@your_username](https://github.com/your_username)

## [1.0.0] - 2021-06-20

### Added

- Inititated y [@your_username](https://github.com/your_username)
- Inititated z [@your_username](https://github.com/your_username)
