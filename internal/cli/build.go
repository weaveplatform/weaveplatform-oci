package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/source"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// sourceRecord is what `source fetch --record` writes and `bundle init
// --source` reads: the build.sourceMedia entry plus how it was verified.
type sourceRecord struct {
	Kind string `json:"kind"`
	source.Result
}

var sourceKinds = map[string]bool{
	"ipsw":        true,
	"iso":         true,
	"esd":         true,
	"cloud-image": true,
	"bootc":       true,
	"package":     true,
}

func newSource(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Fetch upstream build media, verified against the distribution's signed checksums",
	}
	var (
		o            source.Options
		keyring      string
		kind, record string
	)
	fetch := &cobra.Command{
		Use:   "fetch <url> --checksums <url> (--signature <url> | --clearsigned | --allow-unsigned) --out <file>",
		Short: "Download one medium; refuse it unless a pinned key signed the checksum file that names its hash",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !sourceKinds[kind] {
				return fmt.Errorf(
					"%w: --kind must be ipsw, iso, esd, cloud-image, bootc or package",
					errUsage,
				)
			}
			o.URL = args[0]
			if keyring != "" {
				b, err := os.ReadFile(keyring) //nolint:gosec // the operator names the keyring
				if err != nil {
					return fmt.Errorf("%w: %w", errUsage, err)
				}
				o.Keyring = b
			}
			res, err := source.Fetch(cmd.Context(), o)
			if errors.Is(err, source.ErrOptions) {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			if err != nil {
				return err //nolint:wrapcheck // source errors name the medium
			}
			if record != "" {
				b, _ := json.MarshalIndent(sourceRecord{Kind: kind, Result: res}, "", "  ")
				if err := os.WriteFile(record, append(b, '\n'), 0o600); err != nil {
					return fmt.Errorf("write record: %w", err)
				}
			}
			signer := ""
			if res.Signer != "" {
				signer = " by " + res.Signer
			}
			_, err = fmt.Fprintf(
				stdout,
				"%s %s (%d bytes, %s%s)\n",
				res.Digest,
				o.Out,
				res.Size,
				res.Verification,
				signer,
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
	f := fetch.Flags()
	f.StringVar(&o.Checksums, "checksums", "", "URL of the distribution's checksum file")
	f.StringVar(
		&o.Signature,
		"signature",
		"",
		"URL of a detached OpenPGP signature over the checksum file",
	)
	f.BoolVar(&o.Clearsigned, "clearsigned", false, "the checksum file is clearsigned")
	f.StringVar(
		&keyring,
		"keyring",
		"",
		"the distribution's OpenPGP public keys (armored or binary)",
	)
	f.StringSliceVar(
		&o.Fingerprints,
		"fingerprint",
		nil,
		"fingerprint of a key allowed to sign the checksums (repeatable)",
	)
	f.BoolVar(
		&o.AllowUnsigned,
		"allow-unsigned",
		false,
		"accept an unsigned checksum file (recorded as checksum-only)",
	)
	f.StringVar(&o.Name, "name", "", "name in the checksum file (default: the URL's base name)")
	f.StringVar(&o.Out, "out", "", "where to write the medium")
	f.StringVar(
		&kind,
		"kind",
		"cloud-image",
		"source kind recorded in the build: ipsw, iso, esd, cloud-image, bootc or package",
	)
	f.StringVar(
		&record,
		"record",
		"",
		"write the verified source record (JSON) here, for bundle init --source",
	)
	cmd.AddCommand(fetch)
	return cmd
}

type bundleInit struct {
	disks, sources, annotations []string
	guest                       spec.Guest
	fw                          spec.Firmware
	cpuMin, cpuDefault          int64
	memMin, memDefault          string
	prov                        spec.Provisioning
	template, templateRef       string
	created                     string
	version, revision, repoURL  string
	base                        string
	agentName, agentVersion     string
	auxStorage, uefiVars        string
}

func newBundle(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Create bundle directories for pack and publish",
	}
	var b bundleInit
	initCmd := &cobra.Command{
		Use:   "init <dir> --disk <raw> --os <os> --arch <arch> --os-version <v> --os-build <b> [flags]",
		Short: "Write bundle.json for raw disks (moved into <dir>) and the verified source media",
		Args:  usageArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := b.file(args[0])
			if err != nil {
				return err
			}
			if err := pack.WriteBundleFile(args[0], f); err != nil {
				return err //nolint:wrapcheck // pack names the file
			}
			if _, err := pack.LoadBundle(args[0]); err != nil {
				return err //nolint:wrapcheck // pack names the bundle
			}
			_, err = fmt.Fprintf(
				stdout,
				"bundle %s: %s/%s %s (%s), %d disk(s), %d source(s)\n",
				args[0],
				f.Guest.OS,
				f.Guest.Arch,
				f.Guest.OSVersion,
				f.Guest.OSBuild,
				len(f.Disks),
				len(f.Build.SourceMedia),
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
	f := initCmd.Flags()
	f.StringArrayVar(
		&b.disks,
		"disk",
		nil,
		"raw disk file; the first is the system disk, later ones data disks (repeatable)",
	)
	f.StringArrayVar(
		&b.sources,
		"source",
		nil,
		"source record from `source fetch --record` (repeatable)",
	)
	f.StringArrayVar(
		&b.annotations,
		"annotation",
		nil,
		"manifest annotation key=value (repeatable)",
	)
	f.StringVar(&b.guest.OS, "os", "", "guest os: darwin, windows or linux")
	f.StringVar(&b.guest.Arch, "arch", "", "guest architecture: amd64 or arm64")
	f.StringVar(&b.guest.OSVersion, "os-version", "", "operating system version, e.g. 24.04")
	f.StringVar(
		&b.guest.OSBuild,
		"os-build",
		"",
		"operating system build, e.g. a cloud image serial",
	)
	f.StringVar(&b.guest.Distro, "distro", "", "linux distribution, e.g. ubuntu")
	f.StringVar(&b.guest.Edition, "edition", "", "edition, e.g. Pro")
	f.StringVar(
		&b.guest.Variant,
		"variant",
		spec.TierBase,
		"image tier: base, agent, or a further layer such as xcode-26",
	)
	f.StringVar(
		&b.base,
		"base",
		"",
		"for a derived tier, the weave image it was built on: <name>@sha256:<platform manifest>",
	)
	f.StringVar(&b.fw.Type, "firmware", "uefi", "firmware: uefi, bios or apple")
	f.StringVar(
		&b.fw.HardwareModel,
		"hardware-model",
		"",
		"base64 Apple hardware model (never a machine identifier)",
	)
	f.StringVar(
		&b.fw.MinHostOS,
		"min-host-os",
		"",
		"minimum host OS version required by the firmware",
	)
	f.StringVar(
		&b.auxStorage,
		"auxstorage",
		"",
		"Apple auxiliary storage file to carry with the disk",
	)
	f.StringVar(&b.uefiVars, "uefi-vars", "", "optional identity-free UEFI variables file")
	f.StringVar(
		&b.agentName,
		"weaveagent-name",
		"",
		"installed agent name, used with --weaveagent-version",
	)
	f.StringVar(
		&b.agentVersion,
		"weaveagent-version",
		"",
		"installed agent version, used with --weaveagent-name",
	)
	f.BoolVar(&b.fw.SecureBoot, "secure-boot", false, "the guest requires Secure Boot")
	f.StringVar(&b.fw.TPM, "tpm", "none", "TPM: none or required")
	f.Int64Var(&b.cpuMin, "cpu-min", 1, "minimum vCPUs")
	f.Int64Var(&b.cpuDefault, "cpu", 2, "default vCPUs")
	f.StringVar(&b.memMin, "memory-min", "1GiB", "minimum memory (MiB or GiB suffix, or bytes)")
	f.StringVar(&b.memDefault, "memory", "2GiB", "default memory (MiB or GiB suffix, or bytes)")
	f.StringVar(
		&b.prov.DefaultUser,
		"default-user",
		"",
		"account the image is provisioned with, if any",
	)
	f.StringVar(
		&b.prov.CredentialHint,
		"credential-hint",
		"cloud-init",
		"how consumers gain access, e.g. cloud-init",
	)
	f.StringVar(
		&b.template,
		"template",
		"",
		"image definition, e.g. images/linux/ubuntu-24.04-base",
	)
	f.StringVar(&b.templateRef, "template-ref", "", "repository@commit of the image definition")
	f.StringVar(&b.created, "created", "", "build time, RFC 3339 (default now)")
	f.StringVar(&b.version, "image-version", "", "image version annotation, normally the build tag")
	f.StringVar(
		&b.revision,
		"revision",
		"",
		"revision annotation: the commit of the image definition",
	)
	f.StringVar(
		&b.repoURL,
		"source-url",
		"",
		"source annotation: URL of the repository holding the definition",
	)
	cmd.AddCommand(initCmd)
	return cmd
}

func (b bundleInit) file(dir string) (pack.BundleFile, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, a...))
	}
	if len(b.disks) == 0 {
		return pack.BundleFile{}, bad("at least one --disk is required")
	}
	if (b.agentName == "") != (b.agentVersion == "") {
		return pack.BundleFile{}, bad(
			"--weaveagent-name and --weaveagent-version must be supplied together",
		)
	}
	if b.agentName != "" {
		if b.guest.Variant == spec.TierBase {
			return pack.BundleFile{}, bad("a base image cannot contain an agent")
		}
		b.prov.Agent = &spec.Agent{Name: b.agentName, Version: b.agentVersion}
	}
	if b.auxStorage != "" && b.guest.OS != spec.OSDarwin {
		return pack.BundleFile{}, bad("--auxstorage is only valid for darwin guests")
	}
	if b.template == "" || b.templateRef == "" {
		return pack.BundleFile{}, bad("--template and --template-ref are required")
	}
	// The contract requires these on every manifest (rule 10).
	if b.version == "" || b.revision == "" || b.repoURL == "" {
		return pack.BundleFile{}, bad("--image-version, --revision and --source-url are required")
	}
	created := b.created
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, created); err != nil {
		return pack.BundleFile{}, bad("--created: %v", err)
	}
	memMin, err := parseSize(b.memMin)
	if err != nil {
		return pack.BundleFile{}, bad("--memory-min: %v", err)
	}
	memDefault, err := parseSize(b.memDefault)
	if err != nil {
		return pack.BundleFile{}, bad("--memory: %v", err)
	}
	var base *spec.BaseImage
	if b.base != "" {
		name, dgst, ok := strings.Cut(b.base, "@")
		if !ok || name == "" || dgst == "" {
			return pack.BundleFile{}, bad("--base %q is not <name>@<digest>", b.base)
		}
		base = &spec.BaseImage{Name: name, Digest: dgst}
	}
	f := pack.BundleFile{
		SchemaVersion: 1,
		Guest:         b.guest,
		Firmware:      b.fw,
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: b.cpuMin, Default: b.cpuDefault},
			Memory: spec.MinDefault{Min: memMin, Default: memDefault},
		},
		Provisioning: b.prov,
		Build: spec.Build{
			Template: b.template, TemplateRef: b.templateRef, Base: base,
			SourceMedia: []spec.SourceMedia{}, Created: created,
		},
		Annotations: map[string]string{
			spec.AnnotationVersion:  b.version,
			spec.AnnotationRevision: b.revision,
			spec.AnnotationSource:   b.repoURL,
		},
	}
	for _, kv := range b.annotations {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return pack.BundleFile{}, bad("--annotation %q is not key=value", kv)
		}
		f.Annotations[k] = v
	}
	for _, path := range b.sources {
		raw, err := os.ReadFile(path) //nolint:gosec // the operator names the record
		if err != nil {
			return pack.BundleFile{}, bad("--source: %v", err)
		}
		var r sourceRecord
		if err := json.Unmarshal(
			raw,
			&r,
		); err != nil || r.Kind == "" || r.URI == "" ||
			r.Digest == "" {
			return pack.BundleFile{}, bad("--source %s is not a source record", path)
		}
		f.Build.SourceMedia = append(
			f.Build.SourceMedia,
			spec.SourceMedia{Kind: r.Kind, URI: r.URI, Digest: r.Digest},
		)
	}
	if err := b.movePayload(dir, &f); err != nil {
		return pack.BundleFile{}, err
	}
	return f, nil
}

// movePayload keeps the sparse disks and their carried firmware on the
// output volume; the caller builds all metadata before moving any input.
func (b bundleInit) movePayload(dir string, f *pack.BundleFile) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	for _, state := range []struct {
		name, path string
		required   bool
	}{
		{spec.StateAuxStorage, b.auxStorage, true},
		{spec.StateUEFIVars, b.uefiVars, false},
	} {
		if state.path == "" {
			continue
		}
		name := state.name + ".bin"
		if err := os.Rename(state.path, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("bundle: move state %s: %w", state.name, err)
		}
		f.State = append(f.State, pack.BundleState{
			Name: state.name, Path: name, Semantics: spec.SemanticsCarry, Required: state.required,
		})
	}
	for i, d := range b.disks {
		name, role := "disk"+strconv.Itoa(i), "data"
		if i == 0 {
			role = "system"
		}
		file := name + ".img"
		// Disks are moved, not copied: a raw cloud disk is several GiB and
		// a copy would drop its sparseness on most filesystems.
		if err := os.Rename(d, filepath.Join(dir, file)); err != nil {
			return fmt.Errorf("bundle: move %s into %s: %w", d, dir, err)
		}
		f.Disks = append(f.Disks, pack.BundleDisk{Name: name, Role: role, Path: file})
	}
	return nil
}

// parseSize reads bytes, or a whole number of MiB or GiB.
func parseSize(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "GiB"):
		s, mult = strings.TrimSuffix(s, "GiB"), 1<<30
	case strings.HasSuffix(s, "MiB"):
		s, mult = strings.TrimSuffix(s, "MiB"), 1<<20
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %q is not a positive size", errUsage, s)
	}
	return n * mult, nil
}
