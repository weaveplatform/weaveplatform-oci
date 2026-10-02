@docker
Feature: Artifacts survive real registries (phase 1, early registry loop)
  The contract must hold on the registries weave supports before any client
  code exists: weave-zot (the reference private registry, built from
  deploy/zot) with the referrers API, and CNCF distribution v3 without it.

  Scenario: weave-zot starts healthy from the repository's Dockerfile
    Given a weave-zot registry is running
    Then the registry container is healthy

  Scenario Outline: an artifact round-trips through <registry> unchanged
    Given a <registry> registry is running
    And a darwin arm64 bundle "b" with a zero data disk
    And I run weaveoci "pack {b} --out {src} --tag 26.0-25A354-r1"
    When I copy layout "src" tag "26.0-25A354-r1" to "weave-images/macos-26-vanilla:26.0-25A354-r1" on the registry as "publisher"
    Then the copy succeeds
    When I copy "weave-images/macos-26-vanilla:26.0-25A354-r1" from the registry as "publisher" into layout "dst"
    Then layout "dst" has the same index digest as layout "src"
    When I run weaveoci "inspect {dst} --strict --deep"
    Then the exit code is 0

    Examples:
      | registry     |
      | weave-zot    |
      | distribution |

  Scenario: weave-zot refuses to move an existing tag for the publisher
    Given a weave-zot registry is running
    And a linux amd64 bundle "one"
    And a linux arm64 bundle "two"
    And I run weaveoci "pack {one} --out {a} --tag 24.04-20260915-r1"
    And I run weaveoci "pack {two} --out {b} --tag 24.04-20260915-r1"
    And I copy layout "a" tag "24.04-20260915-r1" to "weave-images/ubuntu-24.04:24.04-20260915-r1" on the registry as "publisher"
    And the copy succeeds
    When I copy layout "b" tag "24.04-20260915-r1" to "weave-images/ubuntu-24.04:24.04-20260915-r1" on the registry as "publisher"
    Then the copy is refused

  Scenario Outline: signature referrers are discoverable on <registry>
    Given a <registry> registry is running
    And a windows amd64 bundle "b"
    And I run weaveoci "pack {b} --out {src} --tag 11-25H2-26200.6584-r1"
    And I copy layout "src" tag "11-25H2-26200.6584-r1" to "weave-images/windows-11-base:11-25H2-26200.6584-r1" on the registry as "publisher"
    And the copy succeeds
    When I attach a signature referrer to "weave-images/windows-11-base:11-25H2-26200.6584-r1" on the registry
    Then the registry <api> the referrers API for "weave-images/windows-11-base:11-25H2-26200.6584-r1"
    And a signature referrer is discoverable for "weave-images/windows-11-base:11-25H2-26200.6584-r1"

    Examples:
      | registry     | api            |
      | weave-zot    | serves         |
      | distribution | does not serve |
