@docker
Feature: Publish, pull and sign in the private profile (phase 2)
  An organisation publishes from any machine with `weaveoci publish`, signing
  with a cosign-format key; consumers pull through the shared cache and
  require the signature. cosign itself accepts what weave signs.

  Background:
    Given the environment variable "COSIGN_PASSWORD" is "acceptance"
    And I run weaveoci "keygen {cosign}"
    And a linux arm64 bundle "arm"
    And a linux amd64 bundle "amd"

  Scenario Outline: publish, verify with cosign, pull and unpack on <registry>
    Given a <registry> registry is running
    And I use the registry as "publisher"
    And a profile file "profiles.yaml":
      """
      schemaVersion: 1
      profiles:
        - name: org
          kind: private
          registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
          signing: {provider: cosign-key, key: cosign.key}
          verify: {mode: signature, publicKeys: [cosign.pub]}
      """
    And the environment variable "WEAVEOCI_PROFILES" is "{profiles.yaml}"
    When I run weaveoci "publish {arm} {amd} --repository <repo>-{run} --tag 24.04-20260915-r1 --promotion-out {entry.json}"
    Then the exit code is 0
    And the output contains "signature sha256:"
    And I remember the published digest as "index"
    And cosign verifies "{registry}/weave-images/<repo>-{run}@{index}" with key "{cosign.pub}"
    When I run weaveoci "pull <repo>-{run}:24.04-20260915-r1 --to {out} --platform linux/amd64"
    Then the exit code is 0
    And the output contains "signature: cosign-key"
    And the file "out/disk0.img" equals the file "amd/disk0.img"
    When I run weaveoci "verify <repo>-{run}:24.04-20260915-r1"
    Then the exit code is 0
    When I run weaveoci "publish {arm} --repository <repo>-{run} --tag 24.04-20260915-r1"
    Then the exit code is 1
    And the error output contains "tag already exists"

    Examples:
      | registry     | repo              |
      | weave-zot    | ubuntu-24.04      |
      | distribution | ubuntu-24.04-dist |

  Scenario: a signature from a key the consumer does not trust is refused
    Given a weave-zot registry is running
    And I use the registry as "publisher"
    And I run weaveoci "keygen {other}"
    And a profile file "profiles.yaml":
      """
      schemaVersion: 1
      default: publisher
      profiles:
        - name: publisher
          kind: private
          registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
          signing: {provider: cosign-key, key: cosign.key}
          verify: {mode: none}
        - name: consumer
          kind: private
          registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
          signing: {provider: cosign-key}
          verify: {mode: signature, publicKeys: [other.pub]}
      """
    And the environment variable "WEAVEOCI_PROFILES" is "{profiles.yaml}"
    And I run weaveoci "publish {arm} --repository untrusted-{run} --tag 1-r1"
    And the exit code is 0
    When I run weaveoci "pull untrusted-{run}:1-r1 --profile consumer"
    Then the exit code is 1
    And the error output contains "not verified"

  Scenario: an unsigned artifact is refused unless verification is explicitly off
    Given a weave-zot registry is running
    And I use the registry as "publisher"
    And a profile file "profiles.yaml":
      """
      schemaVersion: 1
      profiles:
        - name: org
          kind: private
          registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
          signing: {provider: cosign-key, key: cosign.key}
          verify: {mode: signature, publicKeys: [cosign.pub]}
      """
    And the environment variable "WEAVEOCI_PROFILES" is "{profiles.yaml}"
    And I run weaveoci "pack {arm} --out {layout} --tag t"
    And I run weaveoci "push {layout} unsigned-{run}:1-r1"
    And the exit code is 0
    When I run weaveoci "pull unsigned-{run}:1-r1"
    Then the exit code is 1
    And the error output contains "no valid cosign-key signature"
    When I run weaveoci "pull unsigned-{run}:1-r1 --verify none"
    Then the exit code is 0
    When I run weaveoci "sign unsigned-{run}:1-r1"
    Then the exit code is 0
    When I run weaveoci "pull unsigned-{run}:1-r1"
    Then the exit code is 0
