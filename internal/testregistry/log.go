package testregistry

import (
	"io"
	"log"
)

func nopLogger() *log.Logger { return log.New(io.Discard, "", 0) }
