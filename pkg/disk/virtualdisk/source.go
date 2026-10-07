package virtualdisk

import (
	"fmt"
	"io"
	"os"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

// sourceFormat uses container bytes, not the extension. virtdisk's source type
// must be explicit when converting between VHD and VHDX providers.
func sourceFormat(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open source container: %w", err)
	}
	defer f.Close()
	var magic [8]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return "", fmt.Errorf("read source container: %w", err)
	}
	if string(magic[:]) == "vhdxfile" {
		return VHDX, nil
	}
	_, fixed, _, err := vhd.Raw(path)
	if err != nil {
		return "", fmt.Errorf("source must be VHDX or fixed VHD: %w", err)
	}
	if err := fixed.Close(); err != nil {
		return "", fmt.Errorf("close source container: %w", err)
	}
	return FixedVHD, nil
}
