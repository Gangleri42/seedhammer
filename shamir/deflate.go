package shamir

import (
	"fmt"

	df "seedhammer.com/internal/deflate"
)

// deflate compresses data as a raw DEFLATE stream (no zlib header)
// with every back-reference inside the 1 KiB window BBQr's Zlib
// encoding fixes (wbits=10).
func deflate(data []byte) []byte {
	return df.Compress(data)
}

// inflate decompresses a raw DEFLATE stream. A positive limit caps the
// decompressed size in bytes.
func inflate(data []byte, limit int64) ([]byte, error) {
	raw, err := df.Decompress(data, limit)
	if err != nil {
		return nil, fmt.Errorf("shamir: invalid deflate stream: %w", err)
	}
	return raw, nil
}
