package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAdmissionPolicyRequiresExplicitTrustedConfiguration(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "policy.json")
	base := imageAdmissionPolicy{
		Registry:    "ghcr.io",
		TrustedRoot: "root.json",
		Channel:     "stable.json",
		Build:       identityPolicy{Issuer: "issuer", SubjectRegexp: "^build$"},
		Acceptance:  identityPolicy{Issuer: "issuer", SubjectRegexp: "^accept$"},
		Anchors:     []admissionAnchor{{Name: "org", PublicKey: "org.pub"}},
	}
	data, _ := json.Marshal(base)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := loadAdmissionPolicy(file)
	if err != nil || p.TrustedRoot != filepath.Join(dir, "root.json") ||
		p.Anchors[0].PublicKey != filepath.Join(dir, "org.pub") {
		t.Fatal(p, err)
	}
	for _, raw := range []string{`{}`, `{`, string(data) + ` {}`, `{"unknown":1}`} {
		os.WriteFile(file, []byte(raw), 0o600)
		if _, err := loadAdmissionPolicy(file); err == nil {
			t.Fatal(raw)
		}
	}
	for _, mode := range []string{"issuer", "unanchored", "anchor", "absolute", "remote"} {
		p := base
		p.Anchors = append([]admissionAnchor{}, base.Anchors...)
		switch mode {
		case "issuer":
			p.Build.Issuer = ""
		case "unanchored":
			p.Acceptance.SubjectRegexp = ".*"
		case "anchor":
			p.Anchors[0].PublicKey = ""
		case "absolute":
			p.TrustedRoot = filepath.Join(dir, "root.json")
		case "remote":
			p.Channel = "https://example.test/stable.json"
		}
		data, _ := json.Marshal(p)
		os.WriteFile(file, data, 0o600)
		_, err := loadAdmissionPolicy(file)
		if (err == nil) != (mode == "absolute" || mode == "remote") {
			t.Fatal(mode, err)
		}
	}
	if _, err := loadAdmissionPolicy(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing policy accepted")
	}
}
