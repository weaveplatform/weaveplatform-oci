// Package chunk splits a raw guest disk into contract chunk layers and
// reassembles chunk layers into a sparse raw disk (contract §5). Splitting
// detects all-zero chunks and emits the canonical zero chunk for them;
// reassembly verifies every chunk in the contract's order (compressed digest
// and size, uncompressed digest and size, frame content size), skips zero
// chunks, and resumes by re-checking ranges already on disk.
package chunk
