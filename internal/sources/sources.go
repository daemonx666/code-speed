// Package sources links every supported agent into the binary. Each
// subpackage registers itself (source.Register) when imported.
package sources

import (
	_ "github.com/daemonx666/code-speed/internal/source" // Claude Code, Codex
	_ "github.com/daemonx666/code-speed/internal/source/antigravity"
	_ "github.com/daemonx666/code-speed/internal/source/cursor"
	_ "github.com/daemonx666/code-speed/internal/source/droid"
	_ "github.com/daemonx666/code-speed/internal/source/dsh"
	_ "github.com/daemonx666/code-speed/internal/source/gemini"
	_ "github.com/daemonx666/code-speed/internal/source/grok"
	_ "github.com/daemonx666/code-speed/internal/source/hermes"
	_ "github.com/daemonx666/code-speed/internal/source/kimi"
	_ "github.com/daemonx666/code-speed/internal/source/openclaw"
	_ "github.com/daemonx666/code-speed/internal/source/opencode"
	_ "github.com/daemonx666/code-speed/internal/source/pi"
	_ "github.com/daemonx666/code-speed/internal/source/qoder"
	_ "github.com/daemonx666/code-speed/internal/source/workbuddy"
	_ "github.com/daemonx666/code-speed/internal/source/zcode"
)
