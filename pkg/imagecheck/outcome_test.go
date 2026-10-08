package imagecheck

import (
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
)

func currentFixture(os string, agent, desktop bool) (Report, conformance.Report) {
	r, image := fixture(os, agent)
	r.SchemaVersion = 3
	if desktop {
		image.Children[0].Description.Config.Guest.Variant = "desktop"
	}
	cfg := image.Children[0].Description.Config
	for n := range r.Platforms[os+"/arm64"] {
		b := &r.Platforms[os+"/arm64"][n]
		b.Profile = ValidationProfile(cfg)
		if agent {
			b.Operations, b.RebootOperations = map[string]Outcome{}, map[string]Outcome{}
			for _, ops := range []map[string]Outcome{b.Operations, b.RebootOperations} {
				for _, name := range []string{"presence", "exec", "time", "metrics", "clipboard", "session", "display"} {
					ops[name] = Outcome{Status: Passed}
				}
				if b.Profile == "agent-desktop" {
					for _, name := range []string{"clipboard-roundtrip", "display-roundtrip", "desktop-session"} {
						ops[name] = Outcome{Status: Passed}
					}
				}
			}
		}
	}
	return r, image
}

func TestCurrentProfiles(t *testing.T) {
	for _, os := range []string{"linux", "darwin", "windows"} {
		for _, agent := range []bool{false, true} {
			for _, desktop := range []bool{false, true} {
				r, image := currentFixture(os, agent, desktop)
				if err := CheckPromotion(
					encoded(t, r),
					image,
					"r1",
				); (err == nil) != (agent || !desktop) {
					t.Fatal(os, agent, desktop, err)
				}
			}
		}
	}
	old, image := fixture("linux", false)
	if err := CheckPromotion(encoded(t, old), image, "r1"); err == nil {
		t.Fatal("legacy report authorized promotion")
	}
	if err := CheckPromotion([]byte("bad"), image, "r1"); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestOperationOutcomesCannotWeakenDesktopOrReboot(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		for _, reboot := range []bool{false, true} {
			for _, status := range []string{Passed, ExpectedUnavailable, Failed, Skipped, ""} {
				for _, name := range []string{"presence", "exec", "time", "metrics", "clipboard", "session", "display", "extra"} {
					r, image := currentFixture("linux", true, desktop)
					b := &r.Platforms["linux/arm64"][0]
					ops := b.Operations
					if reboot {
						ops = b.RebootOperations
					}
					ops[name] = Outcome{Status: status, Reason: "no graphical session"}
					want := status == Passed ||
						(!desktop && status == ExpectedUnavailable && (name == "clipboard" || name == "display" || name == "session"))
					if err := CheckPromotion(encoded(t, r), image, "r1"); (err == nil) != want {
						t.Fatalf(
							"desktop=%v reboot=%v %s %s: %v",
							desktop,
							reboot,
							name,
							status,
							err,
						)
					}
				}
			}
		}
	}
}

func TestMissingProfileOrDesktopProof(t *testing.T) {
	for _, mode := range []string{"profile", "missing first", "missing reboot", "clipboard-roundtrip", "display-roundtrip", "desktop-session", "reason", "base claims"} {
		t.Run(mode, func(t *testing.T) {
			r, image := currentFixture(
				"linux",
				mode != "base claims",
				mode != "reason" && mode != "base claims",
			)
			b := &r.Platforms["linux/arm64"][0]
			switch mode {
			case "profile":
				b.Profile = "agent-headless"
			case "missing first":
				b.Operations = nil
			case "missing reboot":
				b.RebootOperations = nil
			case "reason":
				b.Operations["clipboard"] = Outcome{Status: ExpectedUnavailable, Reason: " "}
			case "base claims":
				b.Operations = map[string]Outcome{"presence": {Status: Passed}}
			default:
				delete(b.Operations, mode)
			}
			if err := CheckPromotion(encoded(t, r), image, "r1"); err == nil {
				t.Fatal("accepted incomplete evidence")
			}
		})
	}
}

func TestAgentCannotUseBaseOrUnknownTier(t *testing.T) {
	for _, tier := range []string{"base", "custom", ""} {
		r, image := currentFixture("linux", true, false)
		image.Children[0].Description.Config.Guest.Variant = tier
		if err := CheckPromotion(encoded(t, r), image, "r1"); err == nil {
			t.Fatal("accepted incompatible tier", tier)
		}
	}
}
