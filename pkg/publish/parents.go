package publish

import (
	"context"
	"fmt"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

func checkParents(ctx context.Context, r Request, image conformance.Report) error {
	derived := false
	for _, child := range image.Children {
		cfg := child.Description.Config
		if cfg.Build.Base != nil {
			derived = true
		}
		if r.Layout == "" && (cfg.Build.Base != nil || cfg.Provisioning.Agent != nil) {
			return fmt.Errorf(
				"%w: derived image publication requires an accepted layout",
				ErrRequest,
			)
		}
	}
	if !derived {
		return nil
	}
	pol, err := verify.FromProfile(ctx, r.Client.Profile(), "", profile.VerifyChannel, nil)
	if err != nil {
		return fmt.Errorf("parent channel: %w", err)
	}
	m, _, err := channel.Verify(pol.Anchors, *pol.Channel, channel.Options{Now: r.Now})
	if err != nil {
		return fmt.Errorf("parent channel: %w", err)
	}
	if err := imagecheck.CheckParents(image, *m, r.Client.Profile().Registry.Host); err != nil {
		return fmt.Errorf("publication lineage: %w", err)
	}
	return nil
}
