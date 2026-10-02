@docker @phase3
Feature: A Linux cloud image becomes a promoted weave image (phase 3)
  The cloud-image path of decision 0005, end to end through the CLI: the
  distribution's signed checksums vouch for the upstream image, the verified
  image becomes a bundle, and the private profile publishes it to weave-zot,
  signs it with a cosign key and promotes it through the organisation's own
  channel. A consumer with only the channel root and the cosign public key
  pulls exactly the upstream bytes. (The CI workflow adds the qcow2-to-raw
  conversion and a QEMU boot of the real Ubuntu image.)

  Background:
    Given the environment variable "COSIGN_PASSWORD" is "acceptance"
    And I run weaveoci "keygen {cosign}"
    And I run weaveoci "channel keygen root {root}"
    And I run weaveoci "channel keygen signing-2026 {ch/signing}"
    And I run weaveoci "channel endorse {root.key} {ch/signing.pub}"
    And I run weaveoci "channel new org-images {ch/stable.json}"
    And a weave-zot registry is running
    And I use the registry as "publisher"
    And a profile file "profiles.yaml":
      """
      schemaVersion: 1
      profiles:
        - name: org
          kind: private
          registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
          signing: {provider: cosign-key, key: cosign.key}
          verify: {mode: both, publicKeys: [cosign.pub]}
          channel:
            manifest: ch/stable.json
            anchors: [{name: org-root, publicKey: root.pub}]
      """
    And the environment variable "WEAVEOCI_PROFILES" is "{profiles.yaml}"
    And a cloud image server publishes "noble-server-cloudimg-amd64.img" signed by key "ubuntu"

  Scenario: a verified cloud image is published, promoted and pulled unchanged
    When I run weaveoci "source fetch {server}noble-server-cloudimg-amd64.img --checksums {server}SHA256SUMS --signature {server}SHA256SUMS.gpg --keyring {ubuntu.asc} --fingerprint {ubuntu.fpr} --out {work/amd64.raw} --record {work/amd64.json}"
    Then the exit code is 0
    And the output contains "openpgp-detached by {ubuntu.fpr}"
    When I run weaveoci "bundle init {amd64} --disk {work/amd64.raw} --source {work/amd64.json} --os linux --arch amd64 --os-version 24.04 --os-build 20260926 --distro ubuntu --template images/linux/ubuntu-24.04 --template-ref weaveplatform/weaveplatform-oci@acceptance --image-version 24.04-20260926-r1 --revision acceptance --source-url https://github.com/weaveplatform/weaveplatform-oci"
    Then the exit code is 0
    When I run weaveoci "publish {amd64} --repository ubuntu-24.04-{run} --tag 24.04-20260926-r1 --promotion-out {entry.json}"
    Then the exit code is 0
    And I run weaveoci "channel promote {ch/stable.json} {entry.json}"
    And I run weaveoci "channel sign {ch/signing.key} {ch/stable.json}"
    When I run weaveoci "pull ubuntu-24.04-{run}:24.04-20260926-r1 --platform linux/amd64 --to {pulled}"
    Then the exit code is 0
    And the output contains "channel: org-images (anchor org-root, sequence 1)"
    And the output contains "signature: cosign-key"
    And the file "pulled/disk0.img" equals the file "upstream.img"

  Scenario: a checksum file signed by a key the definition does not pin is refused
    Given another signing key "mallory"
    When I run weaveoci "source fetch {server}noble-server-cloudimg-amd64.img --checksums {server}SHA256SUMS --signature {server}SHA256SUMS.gpg --keyring {ubuntu.asc} --fingerprint {mallory.fpr} --out {work/amd64.raw}"
    Then the exit code is 1
    And the error output contains "which is not pinned"

  Scenario: an image altered after its checksums were signed is refused
    Given the server's "noble-server-cloudimg-amd64.img" is altered after signing
    When I run weaveoci "source fetch {server}noble-server-cloudimg-amd64.img --checksums {server}SHA256SUMS --signature {server}SHA256SUMS.gpg --keyring {ubuntu.asc} --fingerprint {ubuntu.fpr} --out {work/amd64.raw}"
    Then the exit code is 1
    And the error output contains "does not match its checksum"
