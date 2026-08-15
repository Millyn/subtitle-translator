//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"unsafe"
)

func LowerProcessPriority() error {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	getCurrentProcess := kernel.NewProc("GetCurrentProcess")
	setPriorityClass := kernel.NewProc("SetPriorityClass")
	h, _, _ := getCurrentProcess.Call()
	const belowNormalPriorityClass = 0x00004000
	ok, _, callErr := setPriorityClass.Call(h, belowNormalPriorityClass)
	if ok == 0 {
		return fmt.Errorf("SetPriorityClass: %v", callErr)
	}
	return nil
}

var _ = unsafe.Pointer(nil)
