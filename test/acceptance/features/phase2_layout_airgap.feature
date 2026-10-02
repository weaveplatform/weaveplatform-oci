@docker
Feature: Air-gapped transfer with signatures intact (phase 2)
  An artifact pulled at a connected site is exported as an OCI layout,
  carried across the air gap, imported into another cache and verified there
  without contacting any registry.

  Scenario: export, import and verify from the cache
    Given the environment variable "COSIGN_PASSWORD" is "acceptance"
    And I run weaveoci "keygen {cosign}"
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
          verify: {mode: signature, publicKeys: [cosign.pub]}
      """
    And the environment variable "WEAVEOCI_PROFILES" is "{profiles.yaml}"
    And a windows amd64 bundle "b"
    And I run weaveoci "publish {b} --repository windows-11-base-{run} --tag 11-25H2-26200.6584-r1"
    And I run weaveoci "pull windows-11-base-{run}:11-25H2-26200.6584-r1"
    When I run weaveoci "export-layout windows-11-base-{run}:11-25H2-26200.6584-r1 {airgap}"
    Then the exit code is 0
    Given the environment variable "WEAVEOCI_CACHE" is "{site-cache}"
    And the environment variable "WEAVE_REGISTRY_PASSWORD" is "wrong-on-purpose"
    When I run weaveoci "import-layout {airgap}"
    Then the exit code is 0
    When I run weaveoci "verify windows-11-base-{run}:11-25H2-26200.6584-r1 --cached"
    Then the exit code is 0
    And the output contains "signature: cosign-key"
    When I run weaveoci "gc --keep 1"
    Then the exit code is 0
    When I run weaveoci "verify windows-11-base-{run}:11-25H2-26200.6584-r1 --cached"
    Then the exit code is 1
