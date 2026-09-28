package patvs

import "time"

const protocolVersion = 1

type discoveryPacket struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	API     int    `json:"api,omitempty"`
}

type ReceiverInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	LastSeen time.Time `json:"last_seen"`
}

type EmitterInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen"`
	Camera    Camera    `json:"camera"`
	Streaming bool      `json:"streaming"`
	LastError string    `json:"last_error,omitempty"`
}

type Camera struct {
	Device    string `json:"device,omitempty"`
	Format    string `json:"format,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	FPS       int    `json:"fps,omitempty"`
	Synthetic bool   `json:"synthetic,omitempty"`
}

type ReceiverStatus struct {
	Identity  Identity               `json:"identity"`
	Emitters  map[string]EmitterInfo `json:"emitters"`
	Streams   map[string]bool        `json:"streams"`
	Playback  string                 `json:"playback,omitempty"`
	PlayerPID int                    `json:"player_pid,omitempty"`
	PlayerErr string                 `json:"player_error,omitempty"`
	Receivers []ReceiverInfo         `json:"receivers,omitempty"`
}

type sessionMessage struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Camera    Camera         `json:"camera,omitempty"`
	Error     string         `json:"error,omitempty"`
	Stream    bool           `json:"stream,omitempty"`
	Request   string         `json:"request,omitempty"`
	Receivers []ReceiverInfo `json:"receivers,omitempty"`
}
