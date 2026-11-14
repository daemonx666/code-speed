// signsums signs a release's SHA256SUMS, so the app's updater can tell the
// file came from whoever holds the release key and not merely from GitHub.
//
//	go run ./build/signsums -gen                 a new key pair
//	CODE_SPEED_SIGNING_KEY=… go run ./build/signsums dist/SHA256SUMS
//
// The key is the base64 ed25519 seed (32 bytes); the signature is written
// beside the file as SHA256SUMS.sig, base64. The public key goes in
// internal/update/signed.go.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/daemonx666/code-speed/internal/update"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-gen" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail(err)
		}
		fmt.Println("private (secret CODE_SPEED_SIGNING_KEY):", base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Println("public (internal/update/signed.go):     ", base64.StdEncoding.EncodeToString(pub))
		return
	}
	if len(os.Args) != 2 {
		fail(fmt.Errorf("usage: signsums -gen | signsums path/to/SHA256SUMS"))
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("CODE_SPEED_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		fail(fmt.Errorf("CODE_SPEED_SIGNING_KEY is not a base64 ed25519 seed"))
	}
	key := ed25519.NewKeyFromSeed(seed)
	sums, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	sig := ed25519.Sign(key, sums)
	// the key must be one the app trusts, or the release would refuse to update
	if err := update.VerifySums(sums, []byte(base64.StdEncoding.EncodeToString(sig))); err != nil {
		fail(fmt.Errorf("signed, but with a key the app doesn't know: %v", err))
	}
	if err := os.WriteFile(os.Args[1]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fail(err)
	}
	fmt.Println("signed", os.Args[1])
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "signsums:", err)
	os.Exit(1)
}
