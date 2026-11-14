package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// PublicKeys are the release keys whose signature over a release's
// SHA256SUMS the updater accepts (SHA256SUMS.sig, made by build/signsums
// in the release workflow). GitHub serves the files and their checksums
// side by side, so the checksums alone only show a file arrived whole;
// the signature shows it is what the key's holder published. A new key is
// added here a release before the old one stops signing.
var PublicKeys = []string{
	"IUE+njZOASGPIdCkKI+Mzk1/SWFMwPh7Xm5hhuA1b/o=",
}

// VerifySums checks sig (base64, as SHA256SUMS.sig holds it) over sums
// against the release keys.
func VerifySums(sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("SHA256SUMS.sig is not a signature")
	}
	for _, k := range PublicKeys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(pub, sums, raw) {
			return nil
		}
	}
	return errors.New("SHA256SUMS isn't signed by the release key")
}
