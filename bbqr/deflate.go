package bbqr

import (
	"fmt"

	df "seedhammer.com/internal/deflate"
)

// deflate compresses data as a raw DEFLATE stream (no zlib header)
// suitable for the EncZlib encoding. The standard fixes wbits=10 so
// decoders need at most 1 KiB of history; internal/deflate keeps every
// back-reference inside that window.
func deflate(data []byte) []byte {
	return df.Compress(data)
}

// inflate decompresses a raw DEFLATE stream. A positive limit caps the
// decompressed size in bytes.
func inflate(data []byte, limit int64) ([]byte, error) {
	raw, err := df.Decompress(data, limit)
	if err != nil {
		return nil, fmt.Errorf("bbqr: invalid zlib stream: %w", err)
	}
	return raw, nil
}
