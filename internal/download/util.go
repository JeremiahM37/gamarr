package download

import (
	"crypto/rand"
	"io"
)

func cryptoReader() io.Reader {
	return rand.Reader
}
