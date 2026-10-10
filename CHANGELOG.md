# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Changed

- go-selfupdate is imported from the Giant Swarm line under its own module path, `github.com/giantswarm/go-selfupdate` v1.6.1 (upstream v1.6.0 plus the patch of creativeprojects/go-selfupdate#58: `github.com/ProtonMail/go-crypto/openpgp` in place of the unmaintained `golang.org/x/crypto/openpgp`), with no `replace`, so GO-2026-5932 is out of the reachable graph of this module and of every CLI that takes it. A CLI switches its own go-selfupdate import to `github.com/giantswarm/go-selfupdate` with the bump: `Install` takes the line's `*selfupdate.Updater`.

### Added

- `make govulncheck`, chained into `make test` and so into CI: a vulnerability reachable from the library's code fails the build here before a CLI inherits it.

- `selfupdatecosign.Install`: `UpdateTo` with a single rename. The binary stays complete while it is replaced, and any number of updates of it can run at once; go-selfupdate's own swap leaves no binary between its two renames, and concurrent updates can remove it.

- `selfupdatecosign.WithIdentity`: pin a different certificate identity, for releases a pipeline other than Giant Swarm's CircleCI signs (a GitHub Actions workflow, for instance).
- `selfupdatecosign.New`: a go-selfupdate `Validator` that installs a release binary only after its cosign Sigstore bundle verifies for a CircleCI build of the given repository.



[Unreleased]: https://github.com/giantswarm/selfupdate-cosign/tree/main
