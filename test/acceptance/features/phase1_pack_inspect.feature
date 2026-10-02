Feature: Pack, inspect and unpack guest artifacts (phase 1)
  A producer turns a bundle of raw disks and state files into a contract v1
  index in an OCI layout; a consumer checks it against the conformance
  checklist and materialises it back into sparse raw disks.

  Scenario Outline: every guest OS packs into a conformant artifact
    Given a <os> <arch> bundle "b" with a zero data disk
    When I run weaveoci "pack {b} --out {layout} --tag 1.0-test-r1"
    Then the exit code is 0
    And the output contains "<platform>"
    When I run weaveoci "inspect {layout} --strict --deep"
    Then the exit code is 0
    And the output contains ": conformant"
    And the output contains "disk disk1 1.00 MiB chunks=1 zero=1"

    Examples:
      | os      | arch  | platform                      |
      | darwin  | arm64 | darwin/arm64 26.0             |
      | windows | amd64 | windows/amd64 10.0.26200.6584 |
      | linux   | arm64 | linux/arm64                   |

  Scenario: unpack reproduces the disk byte for byte, sparse, and resumes
    Given a darwin arm64 bundle "b" with a zero data disk
    And I run weaveoci "pack {b} --out {layout} --tag t"
    When I run weaveoci "unpack {layout} {out}"
    Then the exit code is 0
    And the output contains "fetched=1 zero=1"
    And the file "out/disk0.img" equals the file "b/disk0.img"
    And the file "out/nvram.bin" equals the file "b/nvram.bin"
    When I overwrite 1 MiB at offset 3 MiB of "out/disk0.img"
    And I run weaveoci "unpack {layout} {out} --resume"
    Then the exit code is 0
    And the output contains "fetched=1 zero=0 resumed=1"
    And the file "out/disk0.img" equals the file "b/disk0.img"

  Scenario: a repacked bundle reproduces the same digest
    Given a linux amd64 bundle "b"
    And I run weaveoci "pack {b} --out {layout} --tag t"
    And I run weaveoci "unpack {layout} {out}"
    When I run weaveoci "pack {out} --out {layout2} --tag t"
    Then the exit code is 0
    And layout "layout2" has the same index digest as layout "layout"

  Scenario: a multi-architecture Linux index selects a platform on unpack
    Given a linux arm64 bundle "arm"
    And a linux amd64 bundle "amd"
    When I run weaveoci "pack {arm} {amd} --out {layout} --tag 24.04-20260915-r1"
    Then the exit code is 0
    When I run weaveoci "unpack {layout} {out}"
    Then the exit code is 2
    And the error output contains "--platform"
    When I run weaveoci "unpack {layout} {out} --platform linux/amd64"
    Then the exit code is 0
    And the file "out/disk0.img" equals the file "amd/disk0.img"

  Scenario: a machine identifier in the bundle is refused
    Given a darwin arm64 bundle "b"
    And bundle "b" declares a machine identifier in its firmware
    When I run weaveoci "pack {b} --out {layout}"
    Then the exit code is 1
    And the error output contains "ecid"

  Scenario: guest OS families cannot share an index
    Given a linux amd64 bundle "l"
    And a windows amd64 bundle "w"
    When I run weaveoci "pack {l} {w} --out {layout}"
    Then the exit code is 1
    And the error output contains "rule 5"

  Scenario: deep inspection catches a corrupted chunk
    Given a windows amd64 bundle "b"
    And I run weaveoci "pack {b} --out {layout} --tag t"
    And I corrupt a disk chunk in layout "layout"
    When I run weaveoci "inspect {layout} --strict"
    Then the exit code is 0
    When I run weaveoci "inspect {layout} --strict --deep"
    Then the exit code is 1
    And the output contains "rule 15"

  @contract-size
  Scenario: a contract-size disk has full 512 MiB chunks, a zero chunk and a short tail
    Given a contract-size linux arm64 bundle "b"
    When I run weaveoci "pack {b} --out {layout} --tag t --concurrency 3"
    Then the exit code is 0
    And the output contains "chunks=3 zero=1 size=1.25 GiB"
    When I run weaveoci "unpack {layout} {out} --concurrency 3"
    Then the exit code is 0
    And the file "out/disk0.img" equals the file "b/disk0.img"
    And the file "out/disk0.img" is sparse
