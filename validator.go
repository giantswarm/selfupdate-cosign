// Package selfupdatecosign lets a Giant Swarm command-line tool refuse to
// install a release binary that its own CircleCI pipeline did not build.
//
// The architect orb signs every binary a public Giant Swarm repository
// releases with cosign, keyless: the pipeline's OpenID Connect identity gets a
// short-lived certificate from Fulcio, the signature is recorded in the Rekor
// transparency log and timestamped, and everything a verifier needs travels in
// a Sigstore bundle published next to the binary as <asset>.bundle. Validator
// plugs that check into github.com/creativeprojects/go-selfupdate: DetectLatest
// looks the bundle up (a release without one is reported with
// selfupdate.ErrValidationAssetNotFound and never downloaded), UpdateTo hands
// the downloaded bytes and the bundle to Validate, and only a verified binary
// reaches the disk.
//
// Verification runs against the Sigstore public-good trust root, fetched
// through TUF and cached under ~/.sigstore/root (where cosign keeps it too),
// and requires a Rekor entry, a timestamp, and a certificate that names
// CircleCI as the issuer, a CircleCI pipeline as the subject, and the expected
// GitHub repository as the source repository.
package selfupdatecosign

import (
	"bytes"
	"fmt"
	"regexp"
	"sync"

	"github.com/creativeprojects/go-selfupdate"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	// BundleSuffix is appended to a release asset's name to get the name of
	// the Sigstore bundle the architect orb publishes next to it
	// (muster-linux-amd64 → muster-linux-amd64.bundle).
	BundleSuffix = ".bundle"

	// Issuer is the OpenID Connect issuer of the identity that signs: the
	// CircleCI pipeline. Recorded by Fulcio in the certificate's issuer
	// extension.
	Issuer = "https://oidc.circleci.com"

	// SubjectPattern is the shape of the certificate's subject alternative
	// name: the CircleCI project and pipeline definition that ran the build.
	// The same pattern the orb verifies with right after signing.
	SubjectPattern = `^https://circleci\.com/api/v2/projects/[a-f0-9-]+/pipeline-definitions/[a-f0-9-]+$`
)

var subjectRegexp = regexp.MustCompile(SubjectPattern)

// SourceRepositoryURI is what Fulcio records in the certificate's source
// repository extension (OID 1.3.6.1.4.1.57264.1.12) for a build of the given
// GitHub repository ("owner/name"). Pinning it is what ties a bundle to one
// repository: the subject alone only says that some CircleCI pipeline signed.
func SourceRepositoryURI(repository string) string {
	return "github.com/" + repository
}

// Identity is the certificate identity a bundle for a release of repository
// ("owner/name") must carry: issued to a CircleCI pipeline by CircleCI's OIDC
// issuer, for a build of that repository.
func Identity(repository string) verify.CertificateIdentity {
	return verify.CertificateIdentity{
		SubjectAlternativeName: verify.SubjectAlternativeNameMatcher{Regexp: *subjectRegexp},
		Issuer:                 verify.IssuerMatcher{Issuer: Issuer},
		Extensions:             certificate.Extensions{SourceRepositoryURI: SourceRepositoryURI(repository)},
	}
}

// Validator is a go-selfupdate Validator that accepts a release asset only
// when the Sigstore bundle published next to it verifies for the configured
// repository. Create one with New.
type Validator struct {
	repository string
	identity   verify.CertificateIdentity

	// trusted is the material bundles verify against; loaded on first use and
	// kept, unless loading failed (the next Validate tries again).
	mu       sync.Mutex
	trusted  root.TrustedMaterial
	loadRoot func() (root.TrustedMaterial, error)
}

var _ selfupdate.Validator = (*Validator)(nil)

// Option configures a Validator.
type Option func(*Validator)

// WithTrustedMaterial verifies against the given material instead of the
// Sigstore public-good trust root fetched through TUF. For tests, and for
// environments that pin their own snapshot of the trust root.
func WithTrustedMaterial(material root.TrustedMaterial) Option {
	return func(v *Validator) {
		v.loadRoot = func() (root.TrustedMaterial, error) { return material, nil }
	}
}

// New returns a Validator for releases of the GitHub repository
// ("owner/name"). Pass it as the Validator of a selfupdate.Config:
//
//	selfupdate.NewUpdater(selfupdate.Config{
//		Validator: selfupdatecosign.New("giantswarm/muster"),
//	})
func New(repository string, options ...Option) *Validator {
	v := &Validator{
		repository: repository,
		identity:   Identity(repository),
		loadRoot:   publicGoodTrustedRoot,
	}
	for _, option := range options {
		option(v)
	}
	return v
}

// GetValidationAssetName is the name of the bundle for the release asset:
// the asset's name with BundleSuffix appended. go-selfupdate looks the bundle
// up among the release's assets and fails DetectLatest with
// ErrValidationAssetNotFound when it is missing.
func (v *Validator) GetValidationAssetName(assetName string) string {
	return assetName + BundleSuffix
}

// Validate returns nil when validation is a Sigstore bundle whose signature
// covers release, made under the identity of a CircleCI build of the
// validator's repository, recorded in a transparency log and timestamped; any
// other input is an error, and go-selfupdate leaves the installed binary
// untouched.
func (v *Validator) Validate(assetName string, release, validation []byte) error {
	return v.verify(assetName, verify.WithArtifact(bytes.NewReader(release)), validation)
}

// verify is Validate with the artifact already expressed as a policy: the
// bytes themselves, or (in tests) their digest.
func (v *Validator) verify(assetName string, artifact verify.ArtifactPolicyOption, validation []byte) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(validation); err != nil {
		return fmt.Errorf("%s%s is not a Sigstore bundle: %w", assetName, BundleSuffix, err)
	}
	material, err := v.trustedMaterial()
	if err != nil {
		return fmt.Errorf("loading the Sigstore trust root: %w", err)
	}
	verifier, err := verify.NewVerifier(material,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
		verify.WithSignedCertificateTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("preparing the Sigstore verifier: %w", err)
	}
	policy := verify.NewPolicy(artifact, verify.WithCertificateIdentity(v.identity))
	if _, err := verifier.Verify(&b, policy); err != nil {
		return fmt.Errorf("%s does not verify against %s%s as a CircleCI build of %s: %w",
			assetName, assetName, BundleSuffix, v.repository, err)
	}
	return nil
}

func (v *Validator) trustedMaterial() (root.TrustedMaterial, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.trusted != nil {
		return v.trusted, nil
	}
	material, err := v.loadRoot()
	if err != nil {
		return nil, err
	}
	v.trusted = material
	return material, nil
}

// publicGoodTrustedRoot fetches the current Sigstore public-good trust root
// through TUF: sigstore-go's embedded root of trust, the tuf-repo-cdn.sigstore.dev
// mirror, and a cache under ~/.sigstore/root.
func publicGoodTrustedRoot() (root.TrustedMaterial, error) {
	return root.FetchTrustedRootWithOptions(tuf.DefaultOptions())
}
