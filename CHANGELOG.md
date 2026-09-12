# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- `selfupdatecosign.WithIdentity`: pin a different certificate identity, for releases a pipeline other than Giant Swarm's CircleCI signs (a GitHub Actions workflow, for instance).
- `selfupdatecosign.New`: a go-selfupdate `Validator` that installs a release binary only after its cosign Sigstore bundle verifies for a CircleCI build of the given repository.



[Unreleased]: https://github.com/giantswarm/selfupdate-cosign/tree/main
