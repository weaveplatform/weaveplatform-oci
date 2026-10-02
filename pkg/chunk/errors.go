package chunk

import (
	"bytes"
	"errors"
	"io"
)

var (
	// ErrInvalidInput reports an unusable argument (for example a zero-size disk).
	ErrInvalidInput = errors.New("invalid chunk input")
	// ErrVerify reports a chunk that failed the contract's verification order.
	ErrVerify = errors.New("chunk verification failed")
	// ErrAlreadyExists may be returned by a Sink to say a blob is present.
	ErrAlreadyExists = errors.New("blob already exists")
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
