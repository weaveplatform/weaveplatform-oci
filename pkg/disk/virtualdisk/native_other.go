//go:build !windows

package virtualdisk

func native(request) error { return ErrHost }
