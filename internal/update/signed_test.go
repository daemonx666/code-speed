package update

import (
	"bytes"
	"encoding/base64"
	"os"
	"regexp"
	"testing"
)

// install.sh checks the same signature with OpenSSL, so it carries the
// release key too, as PEM: the two must name the same key.
func TestInstallScriptHasTheReleaseKey(t *testing.T) {
	b, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^PUBKEY='([^']+)'`).FindSubmatch(b)
	if m == nil {
		t.Fatal("install.sh has no PUBKEY")
	}
	der, err := base64.StdEncoding.DecodeString(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	// SubjectPublicKeyInfo for ed25519: a fixed 12-byte header, then the key
	header := []byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}
	if len(der) != 44 || !bytes.Equal(der[:12], header) {
		t.Fatalf("install.sh's PUBKEY is not an ed25519 public key")
	}
	if got := base64.StdEncoding.EncodeToString(der[12:]); got != PublicKeys[0] {
		t.Fatalf("install.sh has key %s, signed.go %s", got, PublicKeys[0])
	}
}
