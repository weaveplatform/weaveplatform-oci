package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/cache"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/client"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
)

// EnvCache overrides the cache directory.
const EnvCache = "WEAVEOCI_CACHE"

// globals are the persistent flags shared by registry commands.
type globals struct {
	profiles string
	profile  string
	cache    string
}

func (g *globals) register(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.StringVar(
		&g.profiles,
		"profiles",
		"",
		"deployment profile file (default $"+profile.EnvProfiles+" or the per-user config dir)",
	)
	f.StringVar(&g.profile, "profile", "", "profile name (default: the file's default)")
	f.StringVar(
		&g.cache,
		"cache",
		"",
		"cache directory (default $"+EnvCache+" or the per-user cache dir)",
	)
}

func (g *globals) selected() (profile.Profile, error) {
	path, err := profile.Path(g.profiles)
	if err != nil {
		return profile.Profile{}, err //nolint:wrapcheck // profile errors are descriptive
	}
	f, err := profile.Load(path)
	if err != nil {
		return profile.Profile{}, err //nolint:wrapcheck // profile errors name the file
	}
	return f.Select(g.profile) //nolint:wrapcheck // profile errors name the profile
}

func (g *globals) client() (*client.Client, profile.Profile, error) {
	p, err := g.selected()
	if err != nil {
		return nil, p, err
	}
	return client.New(p, client.Options{UserAgent: "weaveoci"}), p, nil
}

func (g *globals) openCache(cmd *cobra.Command) (*cache.Store, error) {
	dir := g.cache
	if dir == "" {
		dir = os.Getenv(EnvCache)
	}
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("locate cache: %w", err)
		}
		dir = filepath.Join(base, "weave", "oci-cache")
	}
	s, err := cache.Open(cmd.Context(), dir, cache.Options{})
	if err != nil {
		return nil, err //nolint:wrapcheck // cache errors name the directory
	}
	return s, nil
}
