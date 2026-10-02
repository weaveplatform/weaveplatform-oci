// Package spec implements the weave guest artifact contract v1
// (docs/research/09-artifact-contract-v1.md): media types, annotation keys,
// the config document and its JSON Schema, the canonical zero chunk, and the
// conformance rules that validate an index or a manifest without fetching
// disk chunks.
//
// The package imports nothing else from this module, so consumers that only
// need to inspect or select images (the hostweave server, registry browsers)
// carry no chunking, transport or signing code.
package spec
