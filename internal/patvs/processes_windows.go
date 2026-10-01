package patvs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func enableBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

func mediaCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, windowsExecutable(name), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func windowsExecutable(name string) string {
	if filepath.Base(name) != name {
		return name // Explicit --player paths take precedence.
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	filename := name
	if !strings.HasSuffix(strings.ToLower(filename), ".exe") {
		filename += ".exe"
	}
	var candidates []string
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), filename))
	}
	if strings.EqualFold(filename, "vlc.exe") {
		for _, root := range []string{os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "VideoLAN", "VLC", filename))
			}
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return name // exec.Cmd reports the usual actionable missing-executable error.
}

func listMediaProcesses() []MediaProcess {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// CIM includes command lines; Toolhelp snapshots only provide names and PIDs.
	script := `[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new(); $ErrorActionPreference = 'Stop'; ConvertTo-Json -Compress -InputObject @(Get-CimInstance Win32_Process -Filter "Name = 'ffmpeg.exe' OR Name = 'vlc.exe' OR Name = 'cvlc.exe'" | Select-Object ProcessId, Name, CommandLine)`
	command := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	output, err := mediaCommand(ctx, command, "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil
	}
	var rows []struct {
		ProcessID   int `json:"ProcessId"`
		Name        string
		CommandLine string
	}
	if json.Unmarshal(output, &rows) != nil {
		return nil
	}
	processes := make([]MediaProcess, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSuffix(strings.ToLower(row.Name), ".exe")
		if name == "cvlc" {
			name = "vlc"
		}
		processes = append(processes, MediaProcess{PID: row.ProcessID, Name: name, Command: row.CommandLine})
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes
}
