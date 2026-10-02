@docker @phase2b
Feature: weave-zot behaves as the reference private registry (phase 2b)
  The published weave-zot image carries weave's settings. The private role
  requires authentication, lets the publisher create tags but not move
  them, and lets only the admin move one. The mirror role serves upstream
  content on demand with digests and signature referrers unchanged, which
  is what lets a channel manifest written against GHCR admit an image
  pulled from a site mirror.

  Scenario: the private role refuses anonymous and badly authenticated clients
    Given a weave-zot registry is running
    Then an anonymous request to "/v2/_catalog" is refused
    And a request to "/v2/" as "publisher" with a wrong password is refused
    And a request to "/v2/" as "publisher" succeeds

  Scenario: an anonymous push is refused
    Given a weave-zot registry is running
    And a linux amd64 bundle "b"
    And I run weaveoci "pack {b} --out {src} --tag 24.04-20261002-r1"
    When I copy layout "src" tag "24.04-20261002-r1" to "weave-images/anon-{run}:24.04-20261002-r1" on the registry as "anonymous"
    Then the copy is refused

  Scenario: only the admin may move a tag
    Given a weave-zot registry is running
    And a linux amd64 bundle "one"
    And a linux arm64 bundle "two"
    And I run weaveoci "pack {one} --out {a} --tag 24.04-20261002-r1"
    And I run weaveoci "pack {two} --out {b} --tag 24.04-20261002-r1"
    And I copy layout "a" tag "24.04-20261002-r1" to "weave-images/move-{run}:24.04-20261002-r1" on the registry as "publisher"
    And the copy succeeds
    When I copy layout "b" tag "24.04-20261002-r1" to "weave-images/move-{run}:24.04-20261002-r1" on the registry as "publisher"
    Then the copy is refused
    When I copy layout "b" tag "24.04-20261002-r1" to "weave-images/move-{run}:24.04-20261002-r1" on the registry as "admin"
    Then the copy succeeds
    When I copy "weave-images/move-{run}:24.04-20261002-r1" from the registry as "publisher" into layout "after"
    Then layout "after" has the same index digest as layout "b"

  Scenario: a mirror syncs on demand with digests and referrers preserved
    Given a weave-zot registry is running
    And a darwin arm64 bundle "b"
    And I run weaveoci "pack {b} --out {src} --tag 26.0-25A354-r1"
    And I copy layout "src" tag "26.0-25A354-r1" to "weave-images/mirror-{run}:26.0-25A354-r1" on the registry as "publisher"
    And the copy succeeds
    And I attach a signature referrer to "weave-images/mirror-{run}:26.0-25A354-r1" on the registry
    And a weave-zot mirror of the registry is running
    When I copy "weave-images/mirror-{run}:26.0-25A354-r1" from the mirror as "anonymous" into layout "mirrored"
    Then layout "mirrored" has the same index digest as layout "src"
    And a signature referrer is discoverable for "weave-images/mirror-{run}:26.0-25A354-r1" on the mirror
