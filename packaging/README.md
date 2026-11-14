# Releases and package managers

## The release key

Every release's `SHA256SUMS` is signed with an ed25519 key (`SHA256SUMS.sig`,
made by `build/signsums` in the release workflow). The app's updater and
`install.sh` install a release only when that signature checks out against
the public key in `internal/update/signed.go` (and in `install.sh`, as PEM).

- The private key is the secret `CODE_SPEED_SIGNING_KEY` (the base64 ed25519
  seed). Without it a release still publishes, unsigned, and the app won't
  update itself to it.
- A new pair: `go run ./build/signsums -gen`. To change keys, add the new
  public key to `PublicKeys` (and `install.sh`) one release before the
  workflow signs with it, so copies already installed accept it.
- Every file also gets a GitHub build attestation:
  `gh attestation verify <file> -R daemonx666/code-speed`.

## Package managers

The manifests here are templates. `render.sh` fills them with a release's
version and the checksums from its `SHA256SUMS`:

```sh
packaging/render.sh v1.2.3 SHA256SUMS out/
```

The release workflow (`.github/workflows/release.yml`, jobs `packages` and
`winget`) runs it for every tag, attaches the result to the release as
`code-speed-<tag>-packaging.tar.gz`, and publishes to each channel whose
secret is set. A release never fails for want of one.

| Channel | Users run | One-time setup | Secret |
| --- | --- | --- | --- |
| Homebrew (macOS) | `brew install --cask daemonx666/tap/code-speed` | create the repository `daemonx666/homebrew-tap` (empty, with a `Casks/` folder) | `TAP_TOKEN`: a fine-grained token with contents write on the tap and the bucket |
| Scoop (Windows) | `scoop bucket add code-speed https://github.com/daemonx666/scoop-bucket` then `scoop install code-speed` | create the repository `daemonx666/scoop-bucket` (with a `bucket/` folder) | `TAP_TOKEN` |
| winget (Windows) | `winget install daemonx666.code-speed` | submit the first version by hand: `wingetcreate new` with the two setup URLs, or a PR to microsoft/winget-pkgs with the three files in `winget/` | `WINGET_TOKEN`: a classic token with `public_repo`, for the update PRs |
| AUR (Arch) | `yay -S code-speed-bin` | create an AUR account, add its SSH key, and push `PKGBUILD` and `.SRCINFO` to `ssh://aur@aur.archlinux.org/code-speed-bin.git` once | `AUR_SSH_KEY`: that account's private key |

Updates: the Homebrew cask declares `auto_updates`, since the app installs
its own updates (Settings → System). Copies installed by Scoop, the AUR or
a .deb/.rpm leave updating to their package manager; the app says so and
links to the release instead.

The macOS app is not notarized yet, so Homebrew users who see Gatekeeper
refuse it install with `--no-quarantine` (the cask's caveats say so).
