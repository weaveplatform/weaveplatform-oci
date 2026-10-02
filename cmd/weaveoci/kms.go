package main

// KMS providers for cosign-key signing with awskms://, azurekms://,
// gcpkms:// and hashivault:// key references. They are registered by the
// binary only, so library consumers (hostweave, the guestweave CLIs) do not
// inherit the cloud SDKs.
import (
	_ "github.com/sigstore/sigstore/pkg/signature/kms/aws"
	_ "github.com/sigstore/sigstore/pkg/signature/kms/azure"
	_ "github.com/sigstore/sigstore/pkg/signature/kms/gcp"
	_ "github.com/sigstore/sigstore/pkg/signature/kms/hashivault"
)
