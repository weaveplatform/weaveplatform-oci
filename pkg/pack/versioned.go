package pack

import "github.com/opencontainers/image-spec/specs-go"

func specsVersioned() specs.Versioned { return specs.Versioned{SchemaVersion: 2} }
