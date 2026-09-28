package patvs

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestParseCameraFormatsPrefersSmallestSuitableMJPEG(t *testing.T) {
	listing := `Device Caps      : 0x04200001
        Video Capture
    [0]: 'MJPG' (Motion-JPEG, compressed)
        Size: Discrete 1280x720
            Interval: Discrete 0.033s (30.000 fps)
        Size: Discrete 640x480
            Interval: Discrete 0.033s (30.000 fps)
    [1]: 'YUYV' (YUYV 4:2:2)
        Size: Discrete 640x480
            Interval: Discrete 0.033s (30.000 fps)`
	camera, err := parseCameraFormats("/dev/video0", listing)
	if err != nil {
		t.Fatal(err)
	}
	want := Camera{Device: "/dev/video0", Format: "mjpeg", Width: 640, Height: 480, FPS: 30}
	if !reflect.DeepEqual(camera, want) {
		t.Fatalf("got %#v, want %#v", camera, want)
	}
}

func TestReadJPEGFrames(t *testing.T) {
	input := []byte{1, 2, 0xff, 0xd8, 3, 4, 0xff, 0xd9, 9, 0xff, 0xd8, 5, 0xff, 0xd9}
	var frames [][]byte
	err := readJPEGFrames(context.Background(), bytes.NewReader(input), func(frame []byte) { frames = append(frames, frame) })
	if err == nil {
		t.Fatal("expected EOF")
	}
	want := [][]byte{{0xff, 0xd8, 3, 4, 0xff, 0xd9}, {0xff, 0xd8, 5, 0xff, 0xd9}}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("got %v, want %v", frames, want)
	}
}
