# Shared macOS Setup Assistant state machine

Extracted from `weaveplatform/guestweave-cli-macos` at commit
`1ae4c29`, preserving the observed macOS 26/27 screen fixtures and tests.
The code is owned by the Weave Platform project under the repository license.

This package chooses actions from observed screens. It does not start VMs,
invoke Guestweave or assume a screenshot proves OS identity. OCI's native
builder and Guestweave supply their own capture, input and guest-inspection
adapters. `VerifyIdentity` independently checks the resulting guest.
