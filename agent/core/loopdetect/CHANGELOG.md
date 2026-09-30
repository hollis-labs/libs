# Changelog

All notable changes to go-loopdetect are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Initial extraction of Nanite's `internal/loopdetect`: `Detector`, `Signal`, `Detection`, `Fingerprint`, `New`, `Record`, `Reset`, the `With*` options and the `Default*` constants. This is an extraction ahead of adoption; no application uses the module yet.
- `doc.go`, `ExampleDetector_Record`, `FuzzNormalizeArgs`, and tests pinning the unvalidated-option behavior and per-session suppression.
