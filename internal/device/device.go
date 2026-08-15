// Package device enumerates and selects real system capture devices.
package device

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gen2brain/malgo"
)

var (
	ErrNoDevices = errors.New("未发现可用麦克风，请检查 Windows 麦克风权限和设备连接")
	ErrCancelled = errors.New("已取消选择麦克风")
)

// Info is a capture device returned by the operating system. Index is the
// stable index in the displayed list; ID is the native miniaudio device ID.
type Info struct {
	Index       int
	ID          malgo.DeviceID
	Name        string
	Default     bool
	MaxChannels int
}

// List enumerates actual capture endpoints. Loopback/playback endpoints are
// not included because malgo.Capture is used explicitly.
func List(ctx *malgo.AllocatedContext) ([]Info, error) {
	if ctx == nil {
		return nil, errors.New("音频系统尚未初始化")
	}
	items, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("枚举麦克风失败: %w", err)
	}
	out := make([]Info, 0, len(items))
	for _, d := range items {
		maxChannels := 1
		// The first enumeration is intentionally cheap. Query detailed native
		// formats only to report the actual maximum input channel count.
		if details, detailErr := ctx.DeviceInfo(malgo.Capture, d.ID, malgo.Shared); detailErr == nil {
			for _, format := range details.Formats {
				if int(format.Channels) > maxChannels {
					maxChannels = int(format.Channels)
				}
			}
		}
		out = append(out, Info{
			Index:       len(out),
			ID:          d.ID,
			Name:        strings.TrimSpace(d.Name()),
			Default:     d.IsDefault == 1,
			MaxChannels: maxChannels,
		})
	}
	if len(out) == 0 {
		return nil, ErrNoDevices
	}
	return out, nil
}

// Select displays devices and reads a number. Empty input selects the Windows
// default microphone; q/quit/exit cancels cleanly.
func Select(items []Info) (Info, error) {
	return SelectFrom(items, os.Stdin, os.Stdout)
}

// SelectFrom is the testable form of Select.
func SelectFrom(items []Info, in io.Reader, out io.Writer) (Info, error) {
	if len(items) == 0 {
		return Info{}, ErrNoDevices
	}
	if in == nil || out == nil {
		return Info{}, errors.New("选择界面的输入或输出不可用")
	}

	fmt.Fprintln(out, "\n可用麦克风（来自当前系统的真实输入设备）：")
	defaultIndex := -1
	for _, d := range items {
		mark := ""
		if d.Default {
			mark = " [Windows 默认]"
			defaultIndex = d.Index
		}
		channels := d.MaxChannels
		if channels < 1 {
			channels = 1
		}
		fmt.Fprintf(out, "  %d. %s%s（输入通道: %d）\n", d.Index, d.Name, mark, channels)
	}
	if defaultIndex >= 0 {
		fmt.Fprintf(out, "请输入编号（直接回车使用默认设备 %d，q 取消）: ", defaultIndex)
	} else {
		fmt.Fprint(out, "请输入设备编号（q 取消）: ")
	}

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Info{}, fmt.Errorf("读取设备选择失败: %w", err)
		}
		return Info{}, ErrCancelled
	}
	value := strings.TrimSpace(scanner.Text())
	if value == "" && defaultIndex >= 0 {
		return findByIndex(items, defaultIndex)
	}
	switch strings.ToLower(value) {
	case "q", "quit", "exit":
		return Info{}, ErrCancelled
	}
	index, err := strconv.Atoi(value)
	if err != nil {
		return Info{}, fmt.Errorf("无效设备编号 %q", value)
	}
	return findByIndex(items, index)
}

func findByIndex(items []Info, index int) (Info, error) {
	for _, item := range items {
		if item.Index == index {
			return item, nil
		}
	}
	return Info{}, fmt.Errorf("设备编号 %d 不在列表中", index)
}
