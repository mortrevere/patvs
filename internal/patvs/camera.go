package patvs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	sizePattern   = regexp.MustCompile(`Size: Discrete ([0-9]+)x([0-9]+)`)
	fpsPattern    = regexp.MustCompile(`\(([0-9.]+) fps\)`)
	formatPattern = regexp.MustCompile(`'([A-Z0-9]{4})'`)
)

func selectCamera(ctx context.Context, requested string) (Camera, error) {
	if strings.HasPrefix(requested, "lavfi:") {
		return Camera{Device: strings.TrimPrefix(requested, "lavfi:"), Format: "lavfi", Width: 640, Height: 480, FPS: 25, Synthetic: true}, nil
	}
	if requested != "auto" {
		return inspectCamera(ctx, requested)
	}
	paths, _ := filepath.Glob("/dev/video*")
	sort.Strings(paths)
	var choices []Camera
	for _, path := range paths {
		camera, err := inspectCamera(ctx, path)
		if err == nil {
			choices = append(choices, camera)
		}
	}
	if len(choices) == 0 {
		return Camera{}, fmt.Errorf("no usable V4L2 capture device found")
	}
	sort.SliceStable(choices, func(i, j int) bool {
		iMJPEG, jMJPEG := choices[i].Format == "mjpeg", choices[j].Format == "mjpeg"
		if iMJPEG != jMJPEG {
			return iMJPEG
		}
		iMeets := choices[i].Width >= 640 && choices[i].Height >= 480
		jMeets := choices[j].Width >= 640 && choices[j].Height >= 480
		if iMeets != jMeets {
			return iMeets
		}
		iArea, jArea := choices[i].Width*choices[i].Height, choices[j].Width*choices[j].Height
		if iMeets && iArea != jArea {
			return iArea < jArea
		}
		if !iMeets && iArea != jArea {
			return iArea > jArea
		}
		return choices[i].Device < choices[j].Device
	})
	return choices[0], nil
}

func inspectCamera(ctx context.Context, path string) (Camera, error) {
	command := exec.CommandContext(ctx, "v4l2-ctl", "--device", path, "--all", "--list-formats-ext")
	output, err := command.CombinedOutput()
	if err != nil {
		return Camera{}, fmt.Errorf("inspect %s: %w", path, err)
	}
	text := string(output)
	if !strings.Contains(text, "Video Capture") || strings.Contains(text, "Device Caps      : 0x04a00000") {
		return Camera{}, fmt.Errorf("%s is not a video capture node", path)
	}
	return parseCameraFormats(path, text)
}

func parseCameraFormats(path, text string) (Camera, error) {
	format := ""
	best := Camera{Device: path}
	bestScore := int64(-1)
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.Contains(line, "'MJPG'") {
			format = "mjpeg"
			continue
		}
		if match := formatPattern.FindStringSubmatch(line); match != nil {
			format = strings.ToLower(match[1])
			continue
		}
		match := sizePattern.FindStringSubmatch(line)
		if match == nil || format == "" {
			continue
		}
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		fps := 25
		if index+1 < len(lines) {
			if rate := fpsPattern.FindStringSubmatch(lines[index+1]); rate != nil {
				value, _ := strconv.ParseFloat(rate[1], 64)
				fps = int(value + 0.5)
			}
		}
		meets := width >= 640 && height >= 480
		score := int64(width * height)
		if meets {
			score = 1_000_000_000 - score
		}
		if format == "mjpeg" {
			score += 2_000_000_000
		}
		if score > bestScore {
			bestScore = score
			best = Camera{Device: path, Format: format, Width: width, Height: height, FPS: fps}
		}
	}
	if bestScore < 0 {
		return Camera{}, fmt.Errorf("%s has no supported capture format", path)
	}
	return best, nil
}

type captureManager struct {
	cfg         Config
	camera      Camera
	mu          sync.Mutex
	subscribers map[string]chan []byte
	demand      map[string]bool
	cancel      context.CancelFunc
	lastError   string
	nextFrame   time.Time
}

func newCaptureManager(cfg Config, camera Camera) *captureManager {
	return &captureManager{cfg: cfg, camera: camera, subscribers: make(map[string]chan []byte), demand: make(map[string]bool)}
}

func (m *captureManager) add(key string) chan []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	frames := make(chan []byte, 1)
	m.subscribers[key] = frames
	return frames
}

func (m *captureManager) remove(key string) {
	m.mu.Lock()
	delete(m.subscribers, key)
	delete(m.demand, key)
	m.stopIfIdleLocked()
	m.mu.Unlock()
}

func (m *captureManager) setDemand(key string, active bool) {
	m.mu.Lock()
	m.demand[key] = active
	if active && m.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		go m.captureLoop(ctx)
	} else if !active {
		m.stopIfIdleLocked()
	}
	m.mu.Unlock()
}

func (m *captureManager) stopIfIdleLocked() {
	for _, active := range m.demand {
		if active {
			return
		}
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *captureManager) captureLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := m.captureOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.lastError = err.Error()
		m.mu.Unlock()
		slog.Warn("camera capture failed; retrying", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (m *captureManager) captureOnce(ctx context.Context) error {
	if !m.camera.Synthetic && (m.camera.Width == 0 || m.camera.Device == "auto") {
		camera, err := selectCamera(ctx, "auto")
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.camera = camera
		m.mu.Unlock()
	}
	args := ffmpegArgs(m.camera)
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	err = readJPEGFrames(ctx, stdout, m.broadcast)
	if waitErr := command.Wait(); err == nil {
		err = waitErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		err = fmt.Errorf("ffmpeg stopped")
	}
	if stderr.Len() > 0 {
		return fmt.Errorf("%w: %s", err, tail(stderr.String(), 400))
	}
	return err
}

func ffmpegArgs(camera Camera) []string {
	base := []string{"-hide_banner", "-loglevel", "warning", "-nostdin"}
	if camera.Synthetic {
		base = append(base, "-f", "lavfi", "-i", camera.Device, "-an", "-c:v", "mjpeg", "-q:v", "5")
	} else {
		base = append(base, "-f", "v4l2", "-input_format", camera.Format,
			"-video_size", fmt.Sprintf("%dx%d", camera.Width, camera.Height),
			"-framerate", strconv.Itoa(camera.FPS), "-i", camera.Device, "-an")
		if camera.Format == "mjpeg" {
			base = append(base, "-c:v", "copy")
		} else {
			base = append(base, "-c:v", "mjpeg", "-q:v", "5")
		}
	}
	return append(base, "-f", "image2pipe", "pipe:1")
}

func readJPEGFrames(ctx context.Context, source io.Reader, accept func([]byte)) error {
	reader := bufio.NewReaderSize(source, 64<<10)
	frame := make([]byte, 0, 128<<10)
	inFrame, previousFF := false, false
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if !inFrame {
			if previousFF && value == 0xd8 {
				frame = append(frame[:0], 0xff, 0xd8)
				inFrame = true
			}
			previousFF = value == 0xff
			continue
		}
		frame = append(frame, value)
		if len(frame) > maxFrameSize {
			return fmt.Errorf("camera frame exceeds %d bytes", maxFrameSize)
		}
		if previousFF && value == 0xd9 {
			accept(append([]byte(nil), frame...))
			inFrame, previousFF = false, false
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			continue
		}
		previousFF = value == 0xff
	}
}

func (m *captureManager) broadcast(frame []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if !m.nextFrame.IsZero() && now.Before(m.nextFrame) {
		return
	}
	if m.nextFrame.IsZero() {
		m.nextFrame = now
	}
	for !m.nextFrame.After(now) {
		m.nextFrame = m.nextFrame.Add(time.Second / 25)
	}
	m.lastError = ""
	for key, subscriber := range m.subscribers {
		if !m.demand[key] {
			continue
		}
		select {
		case subscriber <- frame:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- frame:
			default:
			}
		}
	}
}

func tail(value string, length int) string {
	value = strings.TrimSpace(value)
	if len(value) > length {
		return value[len(value)-length:]
	}
	return value
}
