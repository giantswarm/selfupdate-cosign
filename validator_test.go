package selfupdatecosign

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The fixture is a real release: the bundle architect published for
// muster-linux-amd64 of giantswarm/muster v5.13.0 (2026-09-07), verified by
// hand with cosign v3.1.3. The binary itself is 85 MB and stays out of the
// repository; its SHA-256 is what the bundle's signature covers, so the
// offline tests verify by digest. The trust root is the Sigstore public-good
// trusted_root.json as served through TUF on 2026-09-08; its keys cover the
// signing time, so the snapshot keeps verifying this bundle.
const (
	fixtureRepository = "giantswarm/muster"
	fixtureAsset      = "muster-linux-amd64"
	fixtureDigestHex  = "8f6de45dbb982f17f4c4bbba3e7dd6fad09d5f73ec19414aa942de60f09228f1"
	fixtureBundle     = "testdata/muster-v5.13.0-linux-amd64.bundle"
	fixtureTrustRoot  = "testdata/trusted_root.json"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return data
}

func snapshot(t *testing.T) root.TrustedMaterial {
	t.Helper()
	material, err := root.NewTrustedRootFromJSON(read(t, fixtureTrustRoot))
	if err != nil {
		t.Fatalf("loading the trust root snapshot: %v", err)
	}
	return material
}

func digest(t *testing.T, hexDigest string) verify.ArtifactPolicyOption {
	t.Helper()
	raw, err := hex.DecodeString(hexDigest)
	if err != nil {
		t.Fatalf("decoding digest: %v", err)
	}
	return verify.WithArtifactDigest("sha256", raw)
}

func TestBundleIsNamedAfterTheAsset(t *testing.T) {
	v := New(fixtureRepository)
	for asset, want := range map[string]string{
		"muster-linux-amd64":       "muster-linux-amd64.bundle",
		"muster-darwin-arm64":      "muster-darwin-arm64.bundle",
		"muster-windows-amd64.exe": "muster-windows-amd64.exe.bundle",
	} {
		if got := v.GetValidationAssetName(asset); got != want {
			t.Errorf("GetValidationAssetName(%q) = %q, want %q", asset, got, want)
		}
	}
}

func TestARealReleaseBundleVerifies(t *testing.T) {
	v := New(fixtureRepository, WithTrustedMaterial(snapshot(t)))
	if err := v.verify(fixtureAsset, digest(t, fixtureDigestHex), read(t, fixtureBundle)); err != nil {
		t.Fatalf("the published bundle should verify for its own binary: %v", err)
	}
}

func TestATamperedBinaryIsRefused(t *testing.T) {
	v := New(fixtureRepository, WithTrustedMaterial(snapshot(t)))
	// The digest of anything but the signed binary: flip one nibble.
	flipped := "0" + fixtureDigestHex[1:]
	if fixtureDigestHex[0] == '0' {
		flipped = "1" + fixtureDigestHex[1:]
	}
	err := v.verify(fixtureAsset, digest(t, flipped), read(t, fixtureBundle))
	if err == nil {
		t.Fatal("a binary the bundle does not cover must be refused")
	}
	t.Logf("refused: %v", err)

	// The same through the public entry point, with bytes that are not the
	// binary at all.
	err = v.Validate(fixtureAsset, []byte("#!/bin/sh\necho not the binary you signed\n"), read(t, fixtureBundle))
	if err == nil {
		t.Fatal("Validate must refuse bytes the bundle does not cover")
	}
	if !strings.Contains(err.Error(), fixtureRepository) {
		t.Errorf("the error should name the repository the bundle was checked against: %v", err)
	}
}

func TestAnotherRepositorysBundleIsRefused(t *testing.T) {
	// A genuine CircleCI signature, but for a build of a different repository:
	// the subject pattern accepts it, the source repository pin does not.
	v := New("giantswarm/mcp-kubernetes", WithTrustedMaterial(snapshot(t)))
	err := v.verify(fixtureAsset, digest(t, fixtureDigestHex), read(t, fixtureBundle))
	if err == nil {
		t.Fatal("a bundle signed for another repository must be refused")
	}
	if !strings.Contains(err.Error(), SourceRepositoryURI(fixtureRepository)) && !strings.Contains(err.Error(), "SourceRepositoryURI") {
		t.Errorf("the error should point at the source repository mismatch: %v", err)
	}
	t.Logf("refused: %v", err)
}

func TestWhatIsNotABundleIsRefused(t *testing.T) {
	v := New(fixtureRepository, WithTrustedMaterial(snapshot(t)))
	for name, validation := range map[string][]byte{
		"empty":         {},
		"not JSON":      []byte("-----BEGIN CERTIFICATE-----\n"),
		"empty object":  []byte("{}"),
		"wrong content": []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`),
	} {
		err := v.Validate(fixtureAsset, []byte("binary"), validation)
		if err == nil {
			t.Errorf("%s: must be refused", name)
			continue
		}
		if !strings.Contains(err.Error(), "is not a Sigstore bundle") {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
	}
}

func TestIdentityPinsIssuerSubjectAndRepository(t *testing.T) {
	identity := Identity(fixtureRepository)
	circleci := "https://circleci.com/api/v2/projects/9dc015e2-2adb-40f3-8c00-edcb97f367ef/pipeline-definitions/6f155034-4283-5038-beb0-7e93c75137f4"
	ok := certificate.Summary{
		SubjectAlternativeName: circleci,
		Extensions: certificate.Extensions{
			Issuer:              Issuer,
			SourceRepositoryURI: "github.com/giantswarm/muster",
			SourceRepositoryRef: "refs/tags/v5.13.0",
		},
	}
	if err := identity.Verify(ok); err != nil {
		t.Fatalf("a CircleCI build of the repository must match: %v", err)
	}

	for name, change := range map[string]func(*certificate.Summary){
		"another repository":           func(s *certificate.Summary) { s.SourceRepositoryURI = "github.com/giantswarm/mcp-kubernetes" },
		"a fork of the repository":     func(s *certificate.Summary) { s.SourceRepositoryURI = "github.com/someone/muster" },
		"no source repository":         func(s *certificate.Summary) { s.SourceRepositoryURI = "" },
		"GitHub Actions as the issuer": func(s *certificate.Summary) { s.Issuer = "https://token.actions.githubusercontent.com" },
		"a person as the subject":      func(s *certificate.Summary) { s.SubjectAlternativeName = "someone@giantswarm.io" },
		"a CircleCI URL of another shape": func(s *certificate.Summary) {
			s.SubjectAlternativeName = "https://circleci.com/api/v2/projects/9dc015e2-2adb-40f3-8c00-edcb97f367ef/pipeline-definitions/6f155034-4283-5038-beb0-7e93c75137f4/extra"
		},
	} {
		s := ok
		change(&s)
		if err := identity.Verify(s); err == nil {
			t.Errorf("%s must not match", name)
		}
	}
}

func TestWithIdentityDecidesWhoMayHaveSigned(t *testing.T) {
	// The genuine CircleCI bundle, checked as a GitHub Actions build of the
	// same repository: the option replaces the pin, so it is refused.
	actions := verify.CertificateIdentity{
		SubjectAlternativeName: verify.SubjectAlternativeNameMatcher{
			SubjectAlternativeName: "https://github.com/giantswarm/muster/.github/workflows/release.yml@refs/heads/main",
		},
		Issuer:     verify.IssuerMatcher{Issuer: "https://token.actions.githubusercontent.com"},
		Extensions: certificate.Extensions{SourceRepositoryURI: "https://github.com/giantswarm/muster"},
	}
	v := New(fixtureRepository, WithTrustedMaterial(snapshot(t)), WithIdentity(actions))
	err := v.verify(fixtureAsset, digest(t, fixtureDigestHex), read(t, fixtureBundle))
	if err == nil {
		t.Fatal("a CircleCI bundle must not verify as a GitHub Actions build")
	}
	if !strings.Contains(err.Error(), fixtureRepository) {
		t.Errorf("the error should still name the repository: %v", err)
	}
	t.Logf("refused: %v", err)

	// The default identity, passed explicitly, verifies as before.
	v = New(fixtureRepository, WithTrustedMaterial(snapshot(t)), WithIdentity(Identity(fixtureRepository)))
	if err := v.verify(fixtureAsset, digest(t, fixtureDigestHex), read(t, fixtureBundle)); err != nil {
		t.Fatalf("the CircleCI identity passed through WithIdentity must verify the published bundle: %v", err)
	}
}

// TestLivePublishedAssetVerifiesWithThePublicTrustRoot downloads a real
// release binary and its bundle from GitHub and verifies them the way a CLI
// does in production: through Validate, against the trust root fetched via
// TUF. It needs the network and ~12 MB, so it runs only when
// SELFUPDATE_COSIGN_LIVE is set.
func TestLivePublishedAssetVerifiesWithThePublicTrustRoot(t *testing.T) {
	if os.Getenv("SELFUPDATE_COSIGN_LIVE") == "" {
		t.Skip("set SELFUPDATE_COSIGN_LIVE=1 to download a release and verify it against the live trust root")
	}
	const (
		repository = "giantswarm/mcp-debug"
		tag        = "v0.0.130"
		asset      = "mcp-debug-linux-amd64"
	)
	fetch := func(name string) []byte {
		t.Helper()
		url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repository, tag, name)
		resp, err := http.Get(url) //nolint:gosec // test fixture download from a fixed URL
		if err != nil {
			t.Fatalf("downloading %s: %v", url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("downloading %s: HTTP %d", url, resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading %s: %v", url, err)
		}
		return data
	}
	v := New(repository)
	binary, bundleBytes := fetch(asset), fetch(v.GetValidationAssetName(asset))
	sum := sha256.Sum256(binary)
	t.Logf("%s %s: %d bytes, sha256 %x", repository, asset, len(binary), sum)
	if err := v.Validate(asset, binary, bundleBytes); err != nil {
		t.Fatalf("the published binary should verify: %v", err)
	}
	binary[len(binary)/2] ^= 0xff
	if err := v.Validate(asset, binary, bundleBytes); err == nil {
		t.Fatal("a binary with one byte flipped must be refused")
	}
}
