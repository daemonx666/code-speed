# Third-party notices

code-speed includes or is derived from the following MIT-licensed work.

## Agent Speed — price table and measurement rules

`internal/pricing/prices.json` (price table v8, 2026-11-10) and the test
fixtures under `internal/pricing/testdata/` and `internal/source/*/testdata/`
are copied from Agent Speed
(https://github.com/zoefix/agent-speed, `spec/assets/prices.json` and
`spec/fixtures/`, commit cae43de). The log parsing (Claude Code, Codex, Gemini
CLI, Grok Build, Kimi Code, OpenCode, Pi, Oh My Pi), live-rate and cost rules
in `internal/source`, `internal/engine` and `internal/pricing` implement
Agent Speed's specification (`spec/METRICS.md`, `spec/QUOTAS.md`).

    MIT License
    Copyright (c) 2026 zoefix

## magpie — Windows WebView2 handling

The Windows desktop app's WebView2 handling is ported from magpie
(https://github.com/yetone/magpie, commit 27e43ed): the missing-runtime
box, the wait for the profile folder and the retried start
(`internal/gui/webview2*.go`), the mending of failed WebView2 processes
(`internal/gui/webviewmend.go`), the console borrowed by the desktop build
(`internal/cli/console_windows.go`) and the tray .ico writer
(`build/icon/gen.go`).

    MIT License
    Copyright (c) 2026 yetone

## Wails v3

The desktop app is built on Wails (https://github.com/wailsapp/wails).

    MIT License
    Copyright (c) 2018-Present Lea Anthony

## gjson

JSON field extraction uses gjson (https://github.com/tidwall/gjson), with
its dependencies match and pretty by the same author.

    The MIT License (MIT)
    Copyright (c) 2016 Josh Baker

## CodexBar — quota pace

`internal/engine/pace.go` follows the pace rules of CodexBar's
`UsagePace.swift` (https://github.com/steipete/CodexBar, MIT, Copyright (c)
Peter Steinberger): expected use from the time gone in the window, the
2 / 6 / 12-point stages, and the run-out time at the pace so far.

## Token Monitor and CodexBar — Cursor endpoints

No code is copied. How `internal/source/cursor` signs in (the access token
Cursor keeps in `state.vscdb`, sent as the `WorkosCursorSessionToken`
cookie) and which cursor.com endpoints it reads (`/api/usage-summary`,
`/api/dashboard/get-filtered-usage-events`) were learnt from Token Monitor
(https://github.com/Javis603/token-monitor, MIT, Copyright (c) Javis603)
and CodexBar (https://github.com/steipete/CodexBar, MIT, Copyright (c)
Peter Steinberger).

## Other modules linked into the binaries

| Module | License | Copyright |
| --- | --- | --- |
| modernc.org/sqlite | BSD-3-Clause | (c) 2017 The Sqlite Authors (SQLite itself is public domain) |
| modernc.org/libc | BSD-3-Clause | (c) 2017 The Libc Authors |
| modernc.org/mathutil | BSD-3-Clause | (c) 2014 The mathutil Authors |
| modernc.org/memory | BSD-3-Clause | (c) 2017 The Memory Authors |
| github.com/remyoudompheng/bigfft | BSD-3-Clause | (c) 2012 The Go Authors |
| github.com/google/uuid | BSD-3-Clause | (c) 2009, 2014 Google Inc. |
| github.com/dustin/go-humanize | MIT | (c) 2005-2008 Dustin Sallings |
| github.com/ncruces/go-strftime | MIT | (c) 2022 Nuno Cruces |
| github.com/mattn/go-isatty | MIT | (c) Yasuhiro Matsumoto |
| github.com/tidwall/sjson | MIT | (c) 2016 Josh Baker |
| github.com/klauspost/compress (zstd) | BSD-3-Clause | (c) 2012 The Go Authors, (c) 2019 Klaus Post |
| github.com/adrg/xdg | MIT | (c) 2014 Adrian-George Bostan |
| github.com/go-ole/go-ole (Windows) | MIT | (c) 2013-2017 Yasuhiro Matsumoto |
| golang.org/x/sys, golang.org/x/exp | BSD-3-Clause | (c) 2009 The Go Authors |

The BSD 3-Clause texts ship with each module (for example
https://gitlab.com/cznic/sqlite/-/blob/master/LICENSE and
https://cs.opensource.google/go/x/sys/+/master:LICENSE). The Go standard
library and runtime are under the same license.

---

Each MIT-licensed work above is distributed under these terms:

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
