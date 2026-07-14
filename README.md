# publish-test-results

A fast GitHub Action that publishes multi-format test results as a drilldownable
per-test check-run.

![CI](https://github.com/jedi-knights/publish-test-results/actions/workflows/ci.yml/badge.svg)
![Release](https://github.com/jedi-knights/publish-test-results/actions/workflows/release.yml/badge.svg)
![GoReleaser](https://github.com/jedi-knights/publish-test-results/actions/workflows/goreleaser.yml/badge.svg)
![Badge](https://github.com/jedi-knights/publish-test-results/actions/workflows/badge.yml/badge.svg)
[![Coverage](https://img.shields.io/badge/Coverage-97.4%25-brightgreen)](https://jedi-knights.github.io/publish-test-results/?v=9)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> **Status: work in progress.** The scaffolding, intermediate representation,
> and parser dispatcher are in place. Individual dialect parsers, the
> Checks-API publisher, and the filesystem locator land in follow-up commits.
> v1.0.0 will not be tagged until every format in the Features list works
> end-to-end.

## Table of contents

- [Overview](#overview)
- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Usage](#usage)
- [Configuration](#configuration)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Overview

`EnricoMi/publish-unit-test-result-action` and similar tools produce an
aggregate summary — pass/fail counts, maybe a PR comment — but there is no
way to click through to an individual test. `publish-test-results` takes the
same JUnit XML (and nine other input dialects) and turns it into a GitHub
Check Run where **every test is a clickable annotation on the diff**.
Passing tests get green notice pins at their source line; failing tests get
red failure pins at the assertion site.

The binary is written in Go so cold start is 10–50 ms; a typical thousand-
test suite parses and publishes in well under a second on a runner.

## Features

- Ten input dialects, one output:
  - JUnit XML — Surefire dialect (pytest, Jest + jest-junit, gotestsum, ctestprobe)
  - JUnit XML — Ant dialect (Maven Surefire plugin, legacy tools)
  - xUnit v2 XML (.NET)
  - NUnit XML (.NET)
  - TRX (Visual Studio Test / MSTest)
  - TAP v13 and v14 (Perl, Node `tap`, some Rust libs)
  - `go test -json` (Go standard library streaming JSON)
  - Jest JSON (`--json` default reporter, distinct from jest-junit)
  - Vitest JSON (`--reporter=json`)
  - Cucumber JSON
  - SubUnit v2 (Python)
- One Check Run per suite with a markdown table of every test grouped by
  suite name
- Per-test annotations on the Files-changed diff — pass, fail, and skip
- Job Summary mirror for the job-detail panel
- Optional PR comment for at-a-glance triage
- Filesystem locator infers source `file:line` for producers whose XML
  doesn't carry it (pytest, Jest, etc.), so you get clickable annotations
  regardless of the producer
- Cross-platform: linux, darwin, windows × amd64, arm64

## Requirements

- A GitHub Actions runner (any OS the action ships a binary for)
- Read/write permission on `checks` and `pull-requests` in the workflow's
  `permissions` block

## Installation

Add the action to a job step in `.github/workflows/*.yml`:

```yaml
- name: Publish test results
  uses: jedi-knights/publish-test-results@v1
  if: always()
  with:
    files: "**/*junit*.xml"
```

## Usage

Full workflow example:

```yaml
name: CI

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    permissions:
      checks: write
      pull-requests: write
    steps:
      - uses: actions/checkout@v5

      - name: Run tests
        run: pytest --junit-xml=results.xml

      - name: Publish test results
        uses: jedi-knights/publish-test-results@v1
        if: always()
        with:
          files: results.xml
          check-name: "Test Results"
```

Pair with `if: always()` so the action still publishes results when the test
step exits non-zero.

## Configuration

| Input | Description | Default |
|---|---|---|
| `files` | Glob pattern for report files; multiple globs comma-separated. | `**/*.xml` |
| `check-name` | Name shown in the Checks tab. | `Test Results` |
| `include-passed` | Also emit per-test annotations for passing tests (green notice pins on the diff). Defaults off because on a real suite the volume of green pins buries the failures; passing tests remain in the check-run summary table regardless. | `false` |
| `include-skipped` | Also emit per-test annotations for skipped tests. | `false` |
| `source-root` | Directory the locator walks to infer `file:line` for reports without source location. | `.` |
| `no-locator` | Disable filesystem inference of `file:line`. | `false` |
| `github-token` | Token for the Checks API. | `${{ github.token }}` |

## Development

Requires Go 1.26 or newer.

```sh
git clone https://github.com/jedi-knights/publish-test-results.git
cd publish-test-results

go build ./...       # compile every package
go vet ./...         # static analysis
go test -race ./...  # run every test with the race detector
```

Repository layout follows Go's `cmd/` + `internal/` convention:

- `cmd/publish-test-results/` — the action's `main` entrypoint
- `internal/ir/` — normalized `TestResult` intermediate representation
- `internal/parse/` — parser interface, registry, and sniffing dispatcher;
  one file per dialect
- `internal/locate/` — filesystem locator that infers source `file:line`
  for producers that don't carry it
- `internal/publish/` — Checks API driver and Job Summary writer

## Contributing

Issues and pull requests welcome. Open an issue to discuss substantial
changes before writing them.

## License

MIT — see [LICENSE](LICENSE).
