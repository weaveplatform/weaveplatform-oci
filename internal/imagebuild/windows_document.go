package imagebuild

// windowsDocument mirrors the native HCS schema used by the Windows consumer.
// Build-only firmware/TPM state is discarded after generalization. It must never
// be shipped as reusable VM identity.
func windowsDocument(disk, installer, seed, state, pipe string) map[string]any {
	return map[string]any{
		"Owner": "weaveoci", "SchemaVersion": map[string]int{"Major": 2, "Minor": 5},
		"ShouldTerminateOnLastHandleClosed": true,
		"VirtualMachine": map[string]any{
			"Version": map[string]int{"Major": 12, "Minor": 0},
			"Chipset": map[string]any{
				"UseUtc": true,
				"Uefi": map[string]any{
					"Console":                 "ComPort1",
					"ApplySecureBootTemplate": "Apply",
					"SecureBootTemplateId":    "1734c6e8-3154-4dda-ba5f-a874cc483422",
					"StopOnBootFailure":       false,
				},
			},
			"ComputeTopology": map[string]any{
				"Memory":    map[string]any{"SizeInMB": 8192, "AllowOvercommit": true},
				"Processor": map[string]int{"Count": 4},
			},
			"SecuritySettings": map[string]any{
				"EnableTpm": true,
				"Isolation": map[string]any{"IsolationType": "GuestStateOnly", "HclEnabled": true},
			},
			"GuestState": map[string]string{"GuestStateFilePath": state},
			"Services": map[string]any{
				"Shutdown":  map[string]any{},
				"Heartbeat": map[string]any{},
				"Timesync":  map[string]any{},
			},
			"Devices": map[string]any{
				"Scsi": map[string]any{"0": map[string]any{"Attachments": map[string]any{
					"0": map[string]string{"Type": "VirtualDisk", "Path": disk},
					"1": map[string]string{"Type": "Iso", "Path": installer},
					"2": map[string]string{"Type": "Iso", "Path": seed},
				}}},
				"ComPorts": map[string]any{"0": map[string]string{"NamedPipe": pipe}},
				"Keyboard": map[string]any{}, "Mouse": map[string]any{},
			},
		},
	}
}
