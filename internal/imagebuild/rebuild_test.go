package imagebuild

import (
	"slices"
	"testing"

	"github.com/opencontainers/go-digest"
)

func rebuildInput() BuildInputs {
	return BuildInputs{
		Image:        "ubuntu-26.04-agent",
		Platform:     "linux/arm64",
		Tier:         "agent",
		ParentDigest: digest.FromString("parent").String(),
		PackageDigests: []string{
			digest.FromString("core").String(),
			digest.FromString("modules").String(),
		},
		RecipeDigest:  digest.FromString("recipe").String(),
		BuilderDigest: digest.FromString("builder").String(),
	}
}

func TestBuildFingerprintNormalizesAndBindsInputs(t *testing.T) {
	in := rebuildInput()
	original := slices.Clone(in.PackageDigests)
	f, err := BuildFingerprint(in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(original, in.PackageDigests) {
		t.Fatal("mutated caller's lock")
	}
	slices.Reverse(in.PackageDigests)
	in.PackageDigests = append(in.PackageDigests, in.PackageDigests[0])
	g, err := BuildFingerprint(in)
	if err != nil || f != g {
		t.Fatal("order changed fingerprint", err)
	}
	for _, mutate := range []func(*BuildInputs){
		func(v *BuildInputs) { v.ParentDigest = digest.FromString("new-parent").String() },
		func(v *BuildInputs) { v.PackageDigests = []string{digest.FromString("new-core").String()} },
		func(v *BuildInputs) { v.RecipeDigest = digest.FromString("new-recipe").String() },
		func(v *BuildInputs) { v.BuilderDigest = digest.FromString("new-builder").String() },
		func(v *BuildInputs) { v.Platform = "linux/amd64" },
		func(v *BuildInputs) { v.Tier = "desktop" },
	} {
		changed := rebuildInput()
		mutate(&changed)
		g, err := BuildFingerprint(changed)
		if err != nil || f == g {
			t.Fatal("changed input did not rebuild", err)
		}
	}
	base := rebuildInput()
	base.Tier, base.ParentDigest, base.SourceDigest = "base", "", digest.FromString("source").
		String()
	if _, err := BuildFingerprint(base); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildPlansDedupeOnlyAcceptedInputs(t *testing.T) {
	in := rebuildInput()
	f, _ := BuildFingerprint(in)
	accepted := []AcceptedBuild{
		{
			Image:       in.Image,
			Platform:    in.Platform,
			Fingerprint: f,
			IndexDigest: digest.FromString("index").String(),
		},
	}
	plan, err := PlanRebuilds([]BuildInputs{in, in}, nil)
	if err != nil || len(plan) != 1 || plan[0].Action != "build" {
		t.Fatal(plan, err)
	}
	plan, err = PlanRebuilds([]BuildInputs{in}, accepted)
	if err != nil || plan[0].Action != "skip" {
		t.Fatal(plan, err)
	}
	changed := in
	changed.ParentDigest = digest.FromString("new").String()
	plan, err = PlanRebuilds([]BuildInputs{changed}, accepted)
	if err != nil || plan[0].Action != "build" {
		t.Fatal(plan, err)
	}
	if _, err := PlanRebuilds([]BuildInputs{in, changed}, accepted); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	other := in
	other.Platform = "linux/amd64"
	plan, err = PlanRebuilds([]BuildInputs{in, other}, accepted)
	if err != nil || plan[0].Platform != "linux/amd64" {
		t.Fatal(plan, err)
	}
	if compareBuildKey("a", "a") != 0 || compareBuildKey("z", "a") != 1 {
		t.Fatal("sort")
	}
	accepted[0].Fingerprint = "bad"
	if _, err := PlanRebuilds(nil, accepted); err == nil {
		t.Fatal("bad completion record accepted")
	}
}

func TestRebuildRefusesUnpinnedOrIncompleteInputs(t *testing.T) {
	for _, mutate := range []func(*BuildInputs){
		func(v *BuildInputs) { v.Image = "../bad" }, func(v *BuildInputs) { v.Platform = "linux/x64" },
		func(v *BuildInputs) { v.Tier = "unknown" }, func(v *BuildInputs) { v.ParentDigest = "" },
		func(v *BuildInputs) { v.PackageDigests = nil }, func(v *BuildInputs) { v.RecipeDigest = "main" },
		func(v *BuildInputs) { v.Tier = "base" }, func(v *BuildInputs) { v.Tier = "base"; v.ParentDigest = "" },
	} {
		in := rebuildInput()
		mutate(&in)
		if _, err := PlanRebuilds([]BuildInputs{in}, nil); err == nil {
			t.Fatal("incomplete input accepted", in)
		}
	}
}
