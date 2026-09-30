package socket_test

import (
	"bytes"
	"io"
)

// newLineReader returns a reader that yields data followed by a newline,
// standing in for a client stream so frame parsing can be tested directly.
func newLineReader(data []byte) io.Reader {
	return bytes.NewReader(append(data, '\n'))
}
