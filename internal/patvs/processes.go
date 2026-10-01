//go:build !windows

package patvs

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func mediaCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func enableBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

func listMediaProcesses() []MediaProcess {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	processes := make([]MediaProcess, 0)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join("/proc", entry.Name())
		nameBytes, err := os.ReadFile(filepath.Join(path, "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(nameBytes))
		if !strings.Contains(comm, "ffmpeg") && !strings.Contains(comm, "vlc") {
			continue
		}
		args, err := os.ReadFile(filepath.Join(path, "cmdline"))
		if err != nil || len(args) == 0 {
			continue
		}
		first := args
		if end := bytes.IndexByte(args, 0); end >= 0 {
			first = args[:end]
		}
		name := filepath.Base(string(first))
		switch name {
		case "ffmpeg", "vlc", "cvlc":
		case ".vlc-wrapped", "vlc.bin":
			name = "vlc"
		case ".ffmpeg-wrapped":
			name = "ffmpeg"
		default:
			continue
		}
		command := strings.Join(strings.Fields(strings.ReplaceAll(string(args), "\x00", " ")), " ")
		processes = append(processes, MediaProcess{PID: pid, Name: name, Command: command})
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes
}
