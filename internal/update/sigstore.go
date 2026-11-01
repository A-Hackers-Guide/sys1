package update

import (
	"bytes"
	"context"
	"os"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The identity that signs releases: GitHub Actions, running this
// repository's packages workflow for a version tag. A signature by anyone
// else, or by this repository from a branch, is refused.
const (
	signerIssuer = "https://token.actions.githubusercontent.com"
	signerSAN    = `^https://github\.com/A-Hackers-Guide/sys1/\.github/workflows/packages\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`
)

// VerifySigstore checks that bundle is a valid Sigstore signature of
// checksums by the release workflow, logged in Rekor. Sigstore's trust root
// comes through its TUF repository and is cached in cacheDir.
func VerifySigstore(ctx context.Context, cacheDir string, checksums, bundleJSON []byte) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	tr, err := root.FetchTrustedRootWithOptions(tuf.DefaultOptions().WithCachePath(cacheDir).WithContext(ctx))
	if err != nil {
		return err
	}
	v, err := verify.NewVerifier(tr, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	id, err := verify.NewShortCertificateIdentity(signerIssuer, "", "", signerSAN)
	if err != nil {
		return err
	}
	_, err = v.Verify(&b, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(id)))
	return err
}
