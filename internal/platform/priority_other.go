//go:build !windows

package platform

func LowerProcessPriority() error { return nil }
