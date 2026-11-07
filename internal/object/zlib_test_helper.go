package object

import (
	"compress/zlib"
	"io"
)

// newZlibWriter is a tiny indirection so tests can build deliberately corrupt
// loose objects without importing compress/zlib themselves.
func newZlibWriter(w io.Writer) *zlib.Writer { return zlib.NewWriter(w) }
