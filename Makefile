# code-speed build and packaging.
#
#   make cli            terminal-only binary for this machine (no cgo)
#   make build          desktop app binary for this machine
#   make test           vet and test both builds
#   make dev-ui         the panel in a browser at http://127.0.0.1:47811
#   make app dmg        macOS .app and .dmg (on a Mac)
#   make release-cli    terminal builds for every OS and arch (any host)
#   make release-windows  Windows desktop .exe, both arches (any host)
#   make release-linux  Linux desktop build + .deb/.rpm (on Linux, GTK 3 and WebKitGTK 4.1 headers)
#
# Everything lands in dist/.

VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null | tr -cd 'A-Za-z0-9._+-'),dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
NUMVER   = $(shell echo $(VERSION) | sed -E 's/^v//; s/[^0-9.].*//; s/^$$/0.0.0/')
TARGETS  = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

NFPM     = go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
WINRES   = go run github.com/tc-hib/go-winres@v0.3.3

# The desktop app links the system webview through cgo on macOS and Linux.
# macOS 13 is the oldest Go 1.27 supports; Linux uses GTK 3 and WebKitGTK 4.1,
# which older distributions ship, where Wails' default GTK 4 would not.
TAGS = production
ifeq ($(shell uname -s),Darwin)
  export CGO_CFLAGS  = -mmacosx-version-min=13.0
  export CGO_LDFLAGS = -mmacosx-version-min=13.0
endif
ifeq ($(shell uname -s),Linux)
  TAGS += gtk3
endif

.PHONY: build cli test dev-ui icons app dmg release-cli release-windows release-linux release-mac clean dist/mac/code-speed

build:
	go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o code-speed .

cli:
	CGO_ENABLED=0 go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o code-speed .

test:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }
	go vet ./...
	go vet -tags nogui ./...
	GOOS=windows go vet ./...
	go test -race ./...

dev-ui:
	go run -tags nogui ./build/devui

# Icons are generated, then committed; run after changing build/icon/gen.go.
icons:
	go run build/icon/gen.go tray 36 internal/gui/tray.png
	go run build/icon/gen.go dot 64 internal/gui/tray-color.png
	go run build/icon/gen.go dot-ico internal/gui/tray-color.ico
	go run build/icon/gen.go app 512 internal/gui/icon.png
	go run build/icon/gen.go app 1024 build/icon/icon-1024.png

# --- macOS -----------------------------------------------------------------

# A universal binary: both arches built natively by clang, joined by lipo.
dist/mac/code-speed:
	@mkdir -p dist/mac
	GOARCH=arm64 CGO_ENABLED=1 go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o dist/mac/code-speed-arm64 .
	GOARCH=amd64 CGO_ENABLED=1 go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o dist/mac/code-speed-amd64 .
	lipo -create -output $@ dist/mac/code-speed-arm64 dist/mac/code-speed-amd64
	@rm dist/mac/code-speed-arm64 dist/mac/code-speed-amd64

# Menu bar app: LSUIElement, no Dock icon. Signed ad hoc; see README for
# Developer ID signing and notarisation.
app: dist/mac/code-speed
	@rm -rf dist/code-speed.app && mkdir -p dist/code-speed.app/Contents/MacOS dist/code-speed.app/Contents/Resources
	cp dist/mac/code-speed dist/code-speed.app/Contents/MacOS/code-speed
	sed 's/@VERSION@/$(NUMVER)/g' build/darwin/Info.plist > dist/code-speed.app/Contents/Info.plist
	@rm -rf dist/mac/icon.iconset && mkdir -p dist/mac/icon.iconset
	@for s in 16 32 128 256 512; do \
		sips -z $$s $$s build/icon/icon-1024.png --out dist/mac/icon.iconset/icon_$${s}x$${s}.png >/dev/null; \
		sips -z $$((s*2)) $$((s*2)) build/icon/icon-1024.png --out dist/mac/icon.iconset/icon_$${s}x$${s}@2x.png >/dev/null; \
	done
	iconutil -c icns dist/mac/icon.iconset -o dist/code-speed.app/Contents/Resources/code-speed.icns
	cp LICENSE THIRD_PARTY_NOTICES.md dist/code-speed.app/Contents/Resources/
	codesign --force --deep --sign - dist/code-speed.app

dmg: app
	@rm -rf dist/dmg && mkdir -p dist/dmg
	cp -R dist/code-speed.app dist/dmg/
	ln -s /Applications dist/dmg/Applications
	hdiutil create -volname code-speed -srcfolder dist/dmg -ov -format UDZO dist/code-speed-$(VERSION)-macos-universal.dmg
	cd dist && ditto -c -k --keepParent code-speed.app code-speed-$(VERSION)-macos-universal.zip
	@rm -rf dist/dmg

release-mac: dmg

# --- terminal builds (any host) --------------------------------------------

release-cli:
	@mkdir -p dist/cli
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		name=code-speed-cli-$(VERSION)-$$os-$$arch; \
		echo "  $$os/$$arch"; \
		rm -rf dist/cli/$$name && mkdir -p dist/cli/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o dist/cli/$$name/code-speed$$ext . || exit 1; \
		cp LICENSE README.md dist/cli/$$name/; \
		if [ $$os = windows ]; then (cd dist/cli && zip -qr ../$$name.zip $$name); \
		else tar -C dist/cli -czf dist/$$name.tar.gz $$name; fi; \
	done

# --- Windows (any host) ----------------------------------------------------

# Wails reaches WebView2 without cgo, so both arches cross-compile. Linked
# as a GUI program (no console window), with the icon, version and manifest
# (per-monitor DPI, long paths, Windows 10 on) from build/windows/winres.json
# by go-winres. The installer is built by makensis where it is installed.
release-windows:
	@mkdir -p dist/windows
	$(WINRES) make --in build/windows/winres.json --arch amd64,arm64 --out rsrc \
		--file-version $(NUMVER).0 --product-version $(NUMVER).0
	@for arch in amd64 arm64; do \
		echo "  windows/$$arch"; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go build -tags production -trimpath \
			-ldflags="$(LDFLAGS) -H windowsgui" -o dist/windows/$$arch/code-speed.exe . || exit 1; \
		cp LICENSE THIRD_PARTY_NOTICES.md dist/windows/$$arch/; \
		(cd dist/windows/$$arch && zip -qr ../../code-speed-$(VERSION)-windows-$$arch-portable.zip .); \
		if command -v makensis >/dev/null; then \
			makensis -V2 -DVERSION=$(NUMVER) -DARCH=$$arch -DSRC=../../dist/windows/$$arch \
				-DOUT=../../dist/code-speed-$(VERSION)-windows-$$arch-setup.exe build/windows/installer.nsi || exit 1; \
		else echo "  makensis not found: no installer for $$arch, portable zip only"; fi; \
	done
	@rm -f rsrc_windows_*.syso

# --- Linux (native) --------------------------------------------------------

LINUX_ARCH = $(shell go env GOARCH)
release-linux:
	@mkdir -p dist/linux
	go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o dist/linux/code-speed .
	cp build/icon/icon-1024.png dist/linux/code-speed.png
	tar -C dist/linux -czf dist/code-speed-$(VERSION)-linux-$(LINUX_ARCH).tar.gz code-speed code-speed.png \
		-C ../.. LICENSE THIRD_PARTY_NOTICES.md build/linux/code-speed.desktop
	VERSION=$(NUMVER) ARCH=$(LINUX_ARCH) $(NFPM) package -f build/linux/nfpm.yaml -p deb -t dist/
	VERSION=$(NUMVER) ARCH=$(LINUX_ARCH) $(NFPM) package -f build/linux/nfpm.yaml -p rpm -t dist/

clean:
	rm -rf code-speed code-speed.exe dist rsrc_windows_*.syso
