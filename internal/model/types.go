package model

import "time"

// Recording represents a single video file in the archive.
type Recording struct {
	ID         int64
	Slug       string
	Label      string // user-provided description
	Date       string // ISO 8601 date
	MasterPath string // relative to data dir
	DurationMs int64
	Width      int
	Height     int
	FPS        float64
	Codec      string
	Interlaced bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// VideoMeta holds metadata extracted from a video file via ffprobe.
type VideoMeta struct {
	DurationMs int64
	Width      int
	Height     int
	FPS        float64
	Codec      string
	Interlaced bool
}

// FrameSample represents a single extracted frame from a video.
type FrameSample struct {
	ID          int64
	RecordingID int64
	TimestampMs int64
	Pass        string // "scout", "fill", "track"
	FramePath   string // relative to data dir
	Width       int
	Height      int
	Processed   bool
	CreatedAt   time.Time
}

// Detection represents a face detected in a frame.
type Detection struct {
	ID          int64
	FrameID     int64
	RecordingID int64
	TimestampMs int64
	BboxX       float64 // x1 in pixels
	BboxY       float64 // y1 in pixels
	BboxW       float64 // width in pixels
	BboxH       float64 // height in pixels
	Confidence  float64
	Landmarks   string // JSON
	CropPath    string // relative to data dir
	CreatedAt   time.Time
}

// Embedding represents a face embedding vector for a detection.
type Embedding struct {
	ID          int64
	DetectionID int64
	RecordingID int64
	Vector      []byte // float64 slice encoded as little-endian bytes
	ModelUsed   string
	Quality     float64
	CreatedAt   time.Time
}

// Track represents a sequence of linked detections across frames.
type Track struct {
	ID           int64
	RecordingID  int64
	StartMs      int64
	EndMs        int64
	DetectionIDs string // JSON array
	AvgEmbedding []byte
	FrameCount   int
	Confidence   float64
	CreatedAt    time.Time
}

// Cluster represents a group of tracks identified as the same person.
type Cluster struct {
	ID            int64
	RecordingID   *int64 // NULL = cross-video
	TrackIDs      string // JSON array
	CentroidEmb   []byte
	ThumbnailPath string
	IdentityID    *int64
	Status        string // "pending", "confirmed", "rejected"
	CreatedAt     time.Time
}

// Identity represents a named person.
type Identity struct {
	ID            int64
	Name          string
	ReferenceEmbs string // JSON array of embedding IDs
	ThumbnailPath string
	Notes         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Group represents a named collection of identities (e.g., "Harker Family").
type Group struct {
	ID        int64
	Name      string
	Notes     string
	CreatedAt time.Time
}

// GroupSummary is a Group with aggregated stats for list views.
type GroupSummary struct {
	ID          int64
	Name        string
	Notes       string
	MemberCount int
}

// Scene represents a detected shot/scene within a recording.
type Scene struct {
	ID          int64
	RecordingID int64
	StartMs     int64
	EndMs       int64
	Score       float64
	CreatedAt   time.Time
}

// ScenePerson tracks a person's presence within a scene.
type ScenePerson struct {
	SceneID           int64
	IdentityID        int64
	FirstAppearanceMs int64
	TotalTimeMs       int64
}
