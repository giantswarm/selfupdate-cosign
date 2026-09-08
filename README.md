# selfupdate-cosign

[![Go Reference](https://pkg.go.dev/badge/github.com/giantswarm/selfupdate-cosign.svg)](https://pkg.go.dev/github.com/giantswarm/selfupdate-cosign)

A [`go-selfupdate`](https://github.com/creativeprojects/go-selfupdate) `Validator` that lets a Giant Swarm
command-line tool refuse to install a release binary its own CircleCI pipeline did not build.

## What it checks

The [architect orb](https://github.com/giantswarm/architect-orb) signs every binary a public Giant Swarm
repository releases with cosign, keyless, and publishes the signature next to the binary as a
[Sigstore bundle](https://docs.sigstore.dev/about/bundle/) named `<binary>-<os>-<arch>.bundle`.
`selfupdatecosign.New("giantswarm/<repo>")` plugs that into `go-selfupdate`:

- `DetectLatest` looks the `.bundle` asset up; a release without one fails with
  `selfupdate.ErrValidationAssetNotFound` and is never downloaded.
- `UpdateTo` hands the downloaded bytes and the bundle to the validator before anything is written.
  The bundle must verify against the Sigstore public-good trust root (fetched through TUF, cached under
  `~/.sigstore/root`), carry a transparency-log entry and a timestamp, and its certificate must name
  - the issuer `https://oidc.circleci.com`,
  - a subject of the shape `https://circleci.com/api/v2/projects/<uuid>/pipeline-definitions/<uuid>`, and
  - the source repository `github.com/giantswarm/<repo>` (Fulcio extension `1.3.6.1.4.1.57264.1.12`),
    which is what ties a bundle to one repository: the subject alone only says that some CircleCI pipeline signed.

Anything else is an error, and `go-selfupdate` leaves the installed binary untouched.

## Usage

```go
import (
	"github.com/creativeprojects/go-selfupdate"
	selfupdatecosign "github.com/giantswarm/selfupdate-cosign"
)

updater, err := selfupdate.NewUpdater(selfupdate.Config{
	Validator: selfupdatecosign.New("giantswarm/muster"),
})
```

Then use `DetectLatest` and `UpdateTo` as usual. Map `selfupdate.ErrValidationAssetNotFound` to a message that
says the release has no signature bundle, and say in the `UpdateTo` error that the binary on disk is unchanged.

Tests and air-gapped environments can pin their own snapshot of the trust root with
`selfupdatecosign.WithTrustedMaterial(material)`.

## Verifying a bundle by hand

```sh
cosign verify-blob --bundle muster-linux-amd64.bundle \
  --certificate-oidc-issuer-regexp '^https://oidc\.circleci\.com' \
  --certificate-identity-regexp '^https://circleci\.com/api/v2/projects/[a-f0-9-]+/pipeline-definitions/[a-f0-9-]+$' \
  muster-linux-amd64
```

The bundles are cosign v3 bundles (`application/vnd.dev.sigstore.bundle.v0.3+json`); cosign v2 cannot read them.

## Tests

`go test ./...` verifies a real published bundle (muster v5.13.0, `testdata/`) against a snapshot of the trust
root, offline. `SELFUPDATE_COSIGN_LIVE=1 go test ./...` additionally downloads a release binary and verifies it
against the live trust root.
