# Image production has moved

[Imageweave](https://github.com/weaveplatform/imageweave) owns the release catalog,
source acquisition, Packer templates, package locks, agent/desktop construction,
runtime acceptance and destination export orchestration. Its `image` commands
retain the migrated CLI names during the Packer scenario transition.

This repository owns the guest-artifact contract, Imageweave import, OCI
pack/unpack, registry transport, signing and evidence verification. See the
[handoff guide](../docs/imageweave-handoff.md) for the producer/consumer boundary.

Use `imageweave image ...` for migrated construction and qualification commands.
Use `weaveoci image verify-acceptance` or `weaveoci image verify-published` for
admission verification. Native construction alone is never acceptance evidence.

Choose an output directory on a filesystem with sufficient free space. Package source and
firmware pins are reviewed Imageweave inputs; they are not release-channel trust
configuration. Windows and cloud execution remain tracked by OCI incidents
[#38](https://github.com/weaveplatform/weaveplatform-oci/issues/38) and
[#39](https://github.com/weaveplatform/weaveplatform-oci/issues/39).
