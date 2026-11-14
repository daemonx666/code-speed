// Package zstdread decompresses zstd data for parsers: whole files
// (Codex's archived .jsonl.zst) and streams of independent frames
// (DeepSeek Harness's logs). Pure Go (github.com/klauspost/compress).
package zstdread

import (
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// MaxOut bounds how much one file may decompress to.
const MaxOut = 1 << 30

// File decompresses a whole .zst file, every frame in it.
func File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Reader(f)
}

// Reader decompresses everything r holds.
func Reader(r io.Reader) ([]byte, error) {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(MaxOut))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return io.ReadAll(io.LimitReader(d, MaxOut))
}

// Decoder returns a reusable decoder for single frames (DecodeAll).
func Decoder() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(MaxOut))
}
