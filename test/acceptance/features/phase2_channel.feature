@docker
Feature: Promotion through an organisation's own channel (phase 2)
  A private organisation runs its own channel root. Only images promoted into
  the signed channel are accepted; a tampered channel or an image that was
  never promoted is refused, whatever its build-time signature.

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
    And a linux amd64 bundle "b"

  Scenario: a promoted image verifies on signature and channel
    Given I run weaveoci "publish {b} --repository fedora-bootc-46-{run} --tag 46-20260930-r1 --promotion-out {entry.json}"
    And I remember the published digest as "index"
    And I run weaveoci "channel promote {ch/stable.json} {entry.json}"
    And I run weaveoci "channel sign {ch/signing.key} {ch/stable.json}"
    When I run weaveoci "channel verify {ch/stable.json} --anchor {root.pub} --digest {index}"
    Then the exit code is 0
    And the output contains "lists weave-images/fedora-bootc-46-{run}:46-20260930-r1"
    When I run weaveoci "pull fedora-bootc-46-{run}:46-20260930-r1"
    Then the exit code is 0
    And the output contains "channel: org-images (anchor org-root, sequence 1)"
    And the output contains "signature: cosign-key"

  Scenario: a signed image that was never promoted is refused
    Given I run weaveoci "channel sign {ch/signing.key} {ch/stable.json}"
    And I run weaveoci "publish {b} --repository fedora-bootc-46-{run} --tag 46-20260930-r2"
    And the exit code is 0
    When I run weaveoci "pull fedora-bootc-46-{run}:46-20260930-r2"
    Then the exit code is 1
    And the error output contains "not in the channel"

  Scenario: a channel edited after signing is refused
    Given I run weaveoci "publish {b} --repository fedora-bootc-46-{run} --tag 46-20260930-r3 --promotion-out {entry.json}"
    And I run weaveoci "channel promote {ch/stable.json} {entry.json}"
    And I run weaveoci "channel sign {ch/signing.key} {ch/stable.json}"
    And I run weaveoci "channel new forged {ch/stable.json}"
    When I run weaveoci "pull fedora-bootc-46-{run}:46-20260930-r3"
    Then the exit code is 1
    And the error output contains "signature chain does not verify"
