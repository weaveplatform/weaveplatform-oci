package channel_test

import (
	"errors"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
)

func TestEndorsedGarbageAndSortByTag(t *testing.T) {
	c := newChain(t, promoted(t))
	// a garbage "signing key" that the root nevertheless endorsed
	garbage := []byte(`{"schema":1,"key_id":"x","public_key":"AAAA"}`)
	end, _ := channel.Endorse(c.rootKey, garbage)
	b := c.bundle
	b.SigningKey, b.SigningKeySig = garbage, end
	if _, _, err := channel.Verify(
		[]channel.Anchor{c.anchor},
		b,
		channel.Options{},
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal(err)
	}
	// a valid chain whose manifest signature file is unparsable
	b = c.bundle
	b.ManifestSig = []byte("{")
	if _, _, err := channel.Verify(
		[]channel.Anchor{c.anchor},
		b,
		channel.Options{},
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal(err)
	}
	if _, err := channel.Parse([]byte("{")); !errors.Is(err, channel.ErrFormat) {
		t.Fatal(err)
	}
	// two tags in one repository sort by tag
	m, _ := channel.Promote(
		promoted(t),
		channel.Image{
			Repository: "weaveplatform/weave-images/ubuntu-24.04",
			Tag:        "24.04-r0",
			Digest:     digest.FromString("old").String(),
		},
		now,
	)
	parsed, err := channel.Parse(m)
	if err != nil || parsed.Images[0].Tag != "24.04-r0" || parsed.Images[1].Tag != "24.04-r1" {
		t.Fatalf("%v %+v", err, parsed)
	}
}
