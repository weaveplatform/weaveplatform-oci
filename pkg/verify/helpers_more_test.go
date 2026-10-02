package verify_test

import (
	"bytes"
	"io"

	"github.com/opencontainers/image-spec/specs-go"
)

func subjVersioned() specs.Versioned { return specs.Versioned{SchemaVersion: 2} }

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
