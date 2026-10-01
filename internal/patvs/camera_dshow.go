package patvs

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	dshowDevicePattern = regexp.MustCompile(`"(.*)"\s+\(video\)`)
	dshowModePattern   = regexp.MustCompile(`(pixel_format|vcodec)=([a-zA-Z0-9_]+)\s+min s=([0-9]+)x([0-9]+) fps=([0-9.]+) max s=([0-9]+)x([0-9]+) fps=([0-9.]+)`)
)

func selectDShowCamera(ctx context.Context, requested string) (Camera, error) {
	if requested != "auto" {
		return inspectDShowCamera(ctx, strings.TrimPrefix(requested, "dshow:"))
	}
	output, err := dshowListing(ctx, "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	if err != nil {
		return Camera{}, err
	}
	var choices []Camera
	for _, name := range parseDShowDevices(output) {
		camera, err := inspectDShowCamera(ctx, name)
		if err != nil {
			slog.Debug("DirectShow camera unavailable", "device", name, "error", err)
			continue
		}
		choices = append(choices, camera)
	}
	if len(choices) == 0 {
		return Camera{}, fmt.Errorf("no usable DirectShow camera found; check Windows camera privacy settings and FFmpeg DirectShow support: %s", tail(output, 400))
	}
	sort.SliceStable(choices, func(i, j int) bool { return betterCamera(choices[i], choices[j]) })
	return choices[0], nil
}

func dshowListing(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := mediaCommand(ctx, "ffmpeg", append([]string{"-hide_banner", "-nostdin"}, args...)...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("inspect DirectShow camera: %w", ctx.Err())
	}
	// FFmpeg deliberately exits nonzero after listing devices/options.
	if len(output) == 0 && err != nil {
		return "", fmt.Errorf("inspect DirectShow camera: %w", err)
	}
	return string(output), nil
}

func parseDShowDevices(text string) []string {
	var names []string
	video := false
	for _, line := range strings.Split(text, "\n") {
		// Older FFmpeg versions label sections instead of individual devices.
		if strings.Contains(line, "DirectShow video devices") {
			video = true
			continue
		}
		if strings.Contains(line, "DirectShow audio devices") {
			video = false
		}
		if strings.Contains(line, "Alternative name") {
			continue
		}
		if match := dshowDevicePattern.FindStringSubmatch(line); match != nil {
			names = appendUnique(names, match[1])
		} else if video {
			start, end := strings.IndexByte(line, '"'), strings.LastIndexByte(line, '"')
			if start >= 0 && end > start {
				names = appendUnique(names, line[start+1:end])
			}
		}
	}
	return names
}

func inspectDShowCamera(ctx context.Context, name string) (Camera, error) {
	output, err := dshowListing(ctx, "-list_options", "true", "-f", "dshow", "-i", "video="+name)
	if err != nil {
		return Camera{}, err
	}
	return parseDShowFormats(name, output)
}

func parseDShowFormats(name, text string) (Camera, error) {
	var choices []Camera
	for _, match := range dshowModePattern.FindAllStringSubmatch(text, -1) {
		minRate, _ := strconv.ParseFloat(match[5], 64)
		maxRate, _ := strconv.ParseFloat(match[8], 64)
		if minRate <= 0 || maxRate < minRate {
			continue
		}
		rate := math.Max(minRate, math.Min(25, maxRate))
		// Use advertised endpoints: an intermediate size may violate driver steps.
		for _, offset := range []int{3, 6} {
			width, _ := strconv.Atoi(match[offset])
			height, _ := strconv.Atoi(match[offset+1])
			if width <= 0 || height <= 0 {
				continue
			}
			format := match[2]
			if match[1] == "vcodec" && format != "mjpeg" {
				format = "vcodec:" + format
			}
			choices = append(choices, Camera{Device: "dshow:" + name, Format: format,
				Width: width, Height: height, FPS: int(math.Round(rate)), FrameRate: strconv.FormatFloat(rate, 'f', -1, 64)})
		}
	}
	if len(choices) == 0 {
		return Camera{}, fmt.Errorf("%s has no supported DirectShow capture format: %s", name, tail(text, 400))
	}
	sort.SliceStable(choices, func(i, j int) bool { return betterCamera(choices[i], choices[j]) })
	return choices[0], nil
}

func dshowInputArgs(camera Camera) []string {
	args := []string{"-f", "dshow"}
	if camera.Format == "mjpeg" || strings.HasPrefix(camera.Format, "vcodec:") {
		args = append(args, "-vcodec", strings.TrimPrefix(camera.Format, "vcodec:"))
	} else if camera.Format != "" {
		args = append(args, "-pixel_format", camera.Format)
	}
	if camera.Width > 0 && camera.Height > 0 {
		args = append(args, "-video_size", fmt.Sprintf("%dx%d", camera.Width, camera.Height))
	}
	rate := camera.FrameRate
	if rate == "" && camera.FPS > 0 {
		rate = strconv.Itoa(camera.FPS)
	}
	if rate != "" {
		args = append(args, "-framerate", rate)
	}
	return append(args, "-i", "video="+strings.TrimPrefix(camera.Device, "dshow:"))
}
