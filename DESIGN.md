# Video Face Analysis System — Design Document

> Scan digitized VHS/home-video footage, find all people who appear,
> let a user review and assign names, and output timestamped appearance
> data with optional clip exports.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Go CLI / Orchestrator                        │
│  Cobra CLI · SQLite job queue · pipeline stages · clip exporter     │
│  frame sampler (ffmpeg) · segment merger · report generator         │
└────────────┬──────────────────────────────────┬─────────────────────┘
             │ HTTP (localhost)                   │ subprocess
             ▼                                   ▼
┌────────────────────────┐           ┌────────────────────────────┐
│   Python ML Worker     │           │   FFmpeg                   │
│   FastAPI on localhost  │           │   frame extraction         │
│   face detection        │           │   clip export              │
│   face embedding        │           │   interlace/denoise        │
│   face tracking         │           │   thumbnail generation     │
└────────────────────────┘           └────────────────────────────┘
             │
             ▼
┌────────────────────────┐
│  SQLite (WAL mode)     │
│  recordings · jobs     │
│  detections · tracks   │
│  embeddings · clusters │
│  identities · segments │
└────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                    Review UI (Go HTTP server)                        │
│  html/template · embedded static assets · localhost:PORT            │
│  cluster gallery · merge/split · name assignment · segment browser  │
└─────────────────────────────────────────────────────────────────────┘
```

### Why This Shape

Face ML is Python's domain — nothing else comes close. Go is excellent at everything
surrounding it: file I/O, subprocess management, job orchestration, HTTP serving, SQLite,
concurrency. The boundary is clean:

- **Go** owns: CLI, job queue, frame sampling (via ffmpeg), video metadata, cluster
  management, segment merging, clip export, review UI, reporting, database
- **Python** owns: face detection, face embedding, clustering — exposed as a local HTTP API

Communication is HTTP on localhost. The Python worker stays warm (model loading is
expensive at 2-5 seconds). Go manages the worker lifecycle or the user runs it separately.

---

## Language Decision: Go + Python Hybrid

### Why Not Pure Go

| Concern | Reality |
|---------|---------|
| Go face detection libs | `pigo` exists but is toy-quality. No serious face embedding library. |
| CGo bindings to dlib/OpenCV | Fragile build, breaks on macOS updates, CGo overhead, debugging nightmare |
| ONNX Runtime in Go | `onnxruntime-go` exists but immature. No ecosystem for face models. |
| RetinaFace/ArcFace/InsightFace | All Python-native. No Go ports. |

### Why Not Pure Python

| Concern | Reality |
|---------|---------|
| Job queue / resumability | You'd reinvent what Go gives you for free with SQLite + goroutines |
| Subprocess management | Python's subprocess module is weaker than Go's os/exec |
| CLI ergonomics | Cobra >> argparse/click for complex CLIs |
| Type safety | Go catches more at compile time |
| Single binary deployment | Go compiles to one binary; Python needs venv management |

### The Split

```
Go binary: ~95% of the code
Python worker: ~5% of the code, all ML inference
Communication: HTTP on localhost (FastAPI)
Lifecycle: Go spawns Python worker as managed subprocess (or user runs separately)
```

Same pattern as audio-archive's whisper-cli subprocess, but HTTP instead of
subprocess-per-call because model loading takes 2-5 seconds and should happen once.

---

## Component Breakdown

### 1. Video Ingestion

Mirrors audio-archive's `pipeline.IngestFile`.

```go
type Recording struct {
    ID          int64
    Slug        string
    Date        string     // YYYY-MM-DD if known
    Label       string     // user-provided description
    MasterPath  string     // relative to data dir
    DurationMs  int64
    Width       int
    Height      int
    FPS         float64
    Codec       string
    Interlaced  bool       // detected via ffprobe
    CreatedAt   time.Time
}
```

**Idempotency:** If master file already exists at destination, return existing ID with
`Skipped=true`.

**ffprobe extraction:** duration, resolution, fps, codec, interlaced flag (drives
deinterlace decision in frame sampling).

### 2. Frame Sampling

Most performance-critical design decision. Can't process every frame of an hour of 30fps
video (108,000 frames).

**Adaptive Multi-Pass Sampling (v2):**

```
Pass 1: "Scout" — 1 frame every 2 seconds (coarse scan)
  -> detect faces, record timestamps where faces exist

Pass 2: "Fill" — 1 frame every 0.5 seconds in face-present regions
  -> denser sampling where people are actually on screen

Pass 3: "Track" — on-demand, 5 fps in short windows
  -> only when tracker needs to bridge a gap between detections
```

**MVP:** Uniform 0.5 fps. Adaptive passes in v2.

**Why adaptive:** VHS home video is ~60% empty rooms, scenery, camera fumbling. Uniform
high-rate sampling wastes 60% of GPU time on frames with no faces.

**Frame extraction via FFmpeg:**

```bash
# Scout pass: 0.5 fps, deinterlace if needed
ffmpeg -i source.mp4 \
  -vf "yadif=1,scale=640:-1" \
  -r 0.5 \
  -q:v 2 \
  frames/scout_%06d.jpg

# Targeted extraction at specific timestamps:
ffmpeg -ss 00:12:34.500 -i source.mp4 \
  -vf "yadif=1" -frames:v 1 -q:v 2 \
  frames/fill_001234500.jpg
```

**Key decisions:**
- **Deinterlace always for VHS** (`yadif=1` — field-adaptive). Combed frames destroy
  face detection.
- **Downscale for detection** (640px wide). Full VHS resolution (720x480) is fine but
  the extra pixels don't help detection on blurry source.
- **Full resolution for thumbnails** (extracted separately for display).
- **JPEG for frame cache** — fast to write, reasonable size, good enough for embedding.

```go
type FrameSample struct {
    ID          int64
    RecordingID int64
    TimestampMs int64
    Pass        string  // "scout", "fill", "track"
    FramePath   string  // relative path to extracted JPEG
    Width       int
    Height      int
    Processed   bool    // ML worker has processed this frame
}
```

### 3. Face Detection

**Model: RetinaFace (MobileNet backbone) via insightface**

Why RetinaFace:
- Best accuracy on low-quality/profile faces among single-stage detectors
- Handles small faces, occlusion, blur better than MTCNN or Haar cascades
- MobileNet backbone is fast on CPU, very fast on GPU
- Returns face landmarks (5-point) needed for alignment before embedding

**Alternative considered:** SCRFD (from InsightFace) — newer, faster, comparable accuracy.
Either works. RetinaFace has more production mileage.

```go
type Detection struct {
    ID          int64
    FrameID     int64
    RecordingID int64
    TimestampMs int64
    BboxX       float64  // normalized 0-1
    BboxY       float64
    BboxW       float64
    BboxH       float64
    Confidence  float64
    Landmarks   string   // JSON: 5 points (eyes, nose, mouth corners)
    CropPath    string   // path to cropped face image
    EmbeddingID *int64   // populated after embedding step
}
```

**Confidence threshold:** 0.5 for VHS (lower than typical 0.7 — catch more faces, let
clustering filter noise later). Conservative approach: keep low-confidence detections
but flag them.

### 4. Face Embedding

**Model: ArcFace (ResNet-100, trained on MS1MV3) via insightface**

Why ArcFace:
- State-of-the-art face recognition accuracy
- 512-dimensional embedding, well-understood clustering properties
- Robust to moderate quality degradation
- Available as ONNX model, runs on CPU or GPU via onnxruntime

**Pipeline:**
1. Crop face from frame using bbox + margin (20% padding)
2. Align face using 5-point landmarks -> standard 112x112 aligned face
3. Run through ArcFace -> 512-dim float32 vector
4. L2-normalize the embedding

```go
type Embedding struct {
    ID          int64
    DetectionID int64
    RecordingID int64
    Vector      []byte   // 512 x float32 = 2048 bytes, stored as blob
    ModelUsed   string
    Quality     float64  // face quality score (blur, pose angle)
}
```

**Quality scoring:** Before embedding, compute a face quality score (blur detection via
Laplacian variance, pose angle from landmarks). Low-quality embeddings are still stored
but weighted down during clustering.

### 5. Face Tracking

**Purpose:** Link detections across consecutive frames into "tracks" — continuous
appearances of the same face.

**Approach: Simple IoU + Embedding Tracker (not DeepSORT)**

Why not DeepSORT: Overkill for 0.5-2 fps sampled frames. DeepSORT is designed for 30fps
real-time tracking. At our sample rates, faces move significantly between frames.

**Custom tracker logic:**
1. For consecutive sampled frames within the same scene:
   - Compute IoU (intersection over union) of bboxes
   - Compute cosine similarity of embeddings
   - If IoU > 0.3 OR embedding similarity > 0.6 -> same track
2. Scene change detection (histogram difference between frames) breaks tracks

```go
type Track struct {
    ID           int64
    RecordingID  int64
    StartMs      int64
    EndMs        int64
    DetectionIDs string  // JSON array of detection IDs in this track
    AvgEmbedding []byte  // running average of embeddings in track
    FrameCount   int
    Confidence   float64 // average detection confidence
}
```

**Track merging:** After initial tracking, merge tracks that are close in time
(gap < 5 seconds) and have similar embeddings (cosine > 0.7). Handles momentary
look-aways or brief occlusions.

### 6. Clustering / Identity Resolution

**Purpose:** Group tracks across the entire video (or archive) into "this is the same
person."

**Algorithm: HDBSCAN on track-level embeddings**

Why HDBSCAN:
- Does not require pre-specifying number of people (unlike k-means)
- Handles noise points (false detections become outliers, not forced into clusters)
- Works well with cosine distance on normalized embeddings
- Produces a hierarchy — useful for the "split cluster" UI action

**Process:**
1. Compute one representative embedding per track (average of top-5 quality detections)
2. Build distance matrix (cosine distance between all track embeddings)
3. Run HDBSCAN with `min_cluster_size=2`, `min_samples=1`
4. Output: cluster assignments + outliers

```go
type Cluster struct {
    ID            int64
    RecordingID   *int64  // NULL = cross-video cluster
    TrackIDs      string  // JSON array
    CentroidEmb   []byte  // average embedding
    ThumbnailPath string  // best representative face crop
    IdentityID    *int64  // NULL until user assigns
    Status        string  // "pending", "confirmed", "rejected"
}

type Identity struct {
    ID            int64
    Name          string
    ReferenceEmbs string  // JSON array of confirmed embedding IDs
    ThumbnailPath string
    CreatedAt     time.Time
}
```

**Cross-video matching:** After clustering within a single video, every pending
cluster's centroid is compared against every confirmed cluster centroid in the
archive. An identity's reference set is the collection of *all* confirmed
cluster centroids assigned to it — not their average. Matching uses **max
cosine similarity over the reference set**: a new cluster is attributed to
whichever identity has the single closest confirmed centroid, provided that
similarity clears the threshold (default 0.5, auto-apply configurable).

Why max-over-refs instead of averaged centroid:

- **Age progression:** ArcFace is invariant to pose/lighting/expression but
  *not age*. Averaging a baby centroid and an adult centroid produces a
  midpoint vector that matches neither age well. Max-over-refs lets a single
  identity span multiple ages — new baby appearances match the baby
  references, new adult appearances match the adult references.
- **Pose/lighting diversity:** Even within one age range, different poses
  (profile, three-quarter, front) don't average linearly in embedding space.
  Max-over-refs is robust to this.

Enabled in `va run` and `va run-all` by default (`AutoApply: true`).

### 7. User Review UI

**Tech: Go `net/http` server + `html/template` (inline string templates) + HTMX**

Lives in `internal/review/`. Started with the intent to `go:embed` templates; currently
templates are inline `var …Template = \`…\`` constants — works fine while small, can be
migrated if they grow.

Why not React/SPA:
- Local tool, not a web app
- html/template is sufficient for thumbnail grids and forms
- Zero build step, no node_modules
- Ships inside the single `va` binary
- HTMX for interactive bits (rename, merge, reject, detach) without page reloads

#### Routes (as built)

**Pages:**
- `GET /` — home: archive stats (recording/identity/group counts) + recordings table
- `GET /review/{recordingID}` — per-recording cluster review (see below)
- `GET /identities` — grid of every named person with stats + thumbnail
- `GET /identities/{id}` — identity detail page
- `GET /groups` — groups CRUD page (members, create, rename, delete)

**Static mounts:**
- `/static/crops/` → `cfg.CropsDir()` (per-detection face crops)
- `/static/frames/` → `cfg.FramesDir()` (full sampled frames)
- `/static/master/` → `cfg.MastersDir()` (original video files; serves Range requests)

**HTMX actions:**
- `POST /review/{recordingID}/clusters/{clusterID}/name` — assign name, create/link identity
- `POST /review/{recordingID}/clusters/{clusterID}/reject` — mark false positive
- `POST /review/{recordingID}/clusters/merge` — merge src cluster into dst
- `POST /identities/{id}/rename` — rename (auto-merges if the name exists)
- `POST /identities/{id}/delete` — detach clusters → pending, drop segments
- `POST /identities/{id}/clusters/{clusterID}/detach` — unlink a cluster, back to pending
- `POST /identities/merge` — merge src identity into dst
- `POST /groups/create`, `POST /groups/{id}/rename`, `POST /groups/{id}/delete`
- `POST /groups/{id}/add`, `POST /groups/{id}/remove/{identityID}`

Intentionally **not implemented yet:** cluster split (merge is more common; punt until
bad clusters hit).

#### Per-recording review page (`/review/{id}`)

- **Sticky video player** at top. Sourced from `/static/master/...`. Only exposed for
  browser-playable containers (`.mp4`, `.m4v`, `.mov`, `.webm`); other formats show a
  graceful "unavailable" notice.
- **Summary bar** — total / pending / confirmed / rejected cluster counts (computed in
  Go, not template, to avoid fragile `range`-accumulator patterns).
- **Cluster grid** — up to 5 representative thumbnails per cluster (evenly sampled from
  track detections), track count, time range, total screen time, identity pill, status.
- **Thumbnail click → dual action:**
  1. **Source-frame lightbox opens** showing the full detection frame with a green bbox
     drawn around the detected face. Overlay is positioned with `object-fit: contain`
     letterbox math so it works at any viewport size; re-runs on `resize`.
  2. **Video player seeks to the thumbnail's timestamp and pauses.** When the user
     closes the lightbox (ESC / backdrop click / ×), the player is already parked at the
     right moment for scrubbing.
- **Name form (HTMX POST)** — creates or reuses an identity.
- **Merge dropdown (HTMX POST)** — pick another cluster to merge this one into.
- **Reject (HTMX POST)** — with confirm dialog.

**Hash-based jumps:** Hitting `/review/{id}#t=<ms>` auto-seeks and plays. Used by
identity-detail segment rows and could be used by external link-sharing.

#### Identity detail page (`/identities/{id}`)

- Header card: thumbnail + name + aggregate stats (videos, clusters, segments, total
  screen time) + group pills.
- **Appearances-by-recording table** — one row per recording this identity appears in,
  linking to that recording's review page.
- **Cluster grid** — every cluster currently assigned to this identity across the
  archive, with a single representative thumbnail (`cluster.thumbnail_path`) and an
  "Unlink" action that flips the cluster back to `pending` (`DetachClusterFromIdentity`).
- **Segment list** — all segments, grouped visually by recording. Each row links to
  `/review/{recordingID}#t=<startMs>` so clicking jumps straight to that moment in the
  source video.

#### Coordinate-system gotcha (critical for anyone touching the lightbox)

Frame JPEGs are stored at **640×427** (or whatever `MaxWidth=640` in `ExtractFrames`
produces), NOT at the recording's native 720×480. Face detection runs on the downscaled
frame, so bbox pixel coords live in the frame-image coord system.

The lightbox reads `img.naturalWidth/Height` at load time and scales bboxes directly
against that — **do not** try to reconcile against `recordings.width/height`. An earlier
attempt used `recording.width` as the bbox reference, which off-by-a-constant'd every
box. The only "sanity scale" needed is for the displayed image size vs intrinsic size:
`scale = min(r.width/natW, r.height/natH)` then `box.left = offX + bbox.x * scale`.

#### Thumbnail generation

Crops come from the face-detection step and are stored at `crops/{recordingID}/det_….jpg`.
Cluster `thumbnail_path` is chosen during clustering (single representative). Full-frame
lightbox pulls from `frames/{recordingID}/scout_<ms>.jpg`.

### 8. Segment Generation and Reporting

After review is complete:

```go
type Segment struct {
    ID          int64
    RecordingID int64
    IdentityID  int64
    StartMs     int64
    EndMs       int64
    Confidence  float64
}

type PersonReport struct {
    Name         string
    TotalTimeMs  int64
    Segments     []Segment
    SourceFiles  []string
}
```

**Merge logic:** If two segments for the same person are within `gapThresholdMs`
(default: 3000ms), merge into one continuous segment. Prevents choppy output.

### 9. Clip Exporting

```bash
ffmpeg -ss START -to END -i source.mp4 \
  -c:v libx264 -crf 23 -c:a aac \
  -movflags +faststart \
  clips/personname_001_00h12m30s.mp4
```

- Uses stream copy (`-c copy`) when possible for speed, falls back to re-encode
- Adds 1 second padding before/after for context
- Names clips: `{person}_{seq}_{timestamp}.mp4`

---

## Database Schema

```sql
CREATE TABLE recordings (
    id          INTEGER PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    label       TEXT,
    date        TEXT,
    master_path TEXT NOT NULL,
    duration_ms INTEGER,
    width       INTEGER,
    height      INTEGER,
    fps         REAL,
    codec       TEXT,
    interlaced  BOOLEAN DEFAULT FALSE,
    created_at  TEXT DEFAULT (datetime('now')),
    updated_at  TEXT DEFAULT (datetime('now'))
);

CREATE TABLE jobs (
    id           INTEGER PRIMARY KEY,
    recording_id INTEGER NOT NULL REFERENCES recordings(id),
    type         TEXT NOT NULL,  -- 'sample_scout','sample_fill','detect','embed','track','cluster'
    status       TEXT NOT NULL DEFAULT 'pending',
    attempt      INTEGER DEFAULT 0,
    max_retries  INTEGER DEFAULT 3,
    error        TEXT,
    progress     TEXT,  -- JSON: {"frames_done": 500, "frames_total": 1200}
    created_at   TEXT DEFAULT (datetime('now')),
    started_at   TEXT,
    finished_at  TEXT
);

CREATE TABLE frames (
    id           INTEGER PRIMARY KEY,
    recording_id INTEGER NOT NULL REFERENCES recordings(id),
    timestamp_ms INTEGER NOT NULL,
    pass         TEXT NOT NULL,  -- 'scout', 'fill', 'track'
    frame_path   TEXT NOT NULL,
    width        INTEGER,
    height       INTEGER,
    processed    BOOLEAN DEFAULT FALSE,
    created_at   TEXT DEFAULT (datetime('now')),
    UNIQUE(recording_id, timestamp_ms)
);

CREATE TABLE detections (
    id           INTEGER PRIMARY KEY,
    frame_id     INTEGER NOT NULL REFERENCES frames(id),
    recording_id INTEGER NOT NULL,
    timestamp_ms INTEGER NOT NULL,
    bbox_x       REAL NOT NULL,
    bbox_y       REAL NOT NULL,
    bbox_w       REAL NOT NULL,
    bbox_h       REAL NOT NULL,
    confidence   REAL NOT NULL,
    landmarks    TEXT,  -- JSON
    crop_path    TEXT,
    created_at   TEXT DEFAULT (datetime('now'))
);

CREATE TABLE embeddings (
    id           INTEGER PRIMARY KEY,
    detection_id INTEGER NOT NULL UNIQUE REFERENCES detections(id),
    recording_id INTEGER NOT NULL,
    vector       BLOB NOT NULL,  -- 512 x float32 = 2048 bytes
    model_used   TEXT NOT NULL,
    quality      REAL,
    created_at   TEXT DEFAULT (datetime('now'))
);

CREATE TABLE tracks (
    id            INTEGER PRIMARY KEY,
    recording_id  INTEGER NOT NULL,
    start_ms      INTEGER NOT NULL,
    end_ms        INTEGER NOT NULL,
    detection_ids TEXT NOT NULL,  -- JSON array
    avg_embedding BLOB,
    frame_count   INTEGER,
    confidence    REAL,
    created_at    TEXT DEFAULT (datetime('now'))
);

CREATE TABLE clusters (
    id             INTEGER PRIMARY KEY,
    recording_id   INTEGER,  -- NULL for cross-video
    track_ids      TEXT NOT NULL,  -- JSON array
    centroid_emb   BLOB,
    thumbnail_path TEXT,
    identity_id    INTEGER REFERENCES identities(id),
    status         TEXT NOT NULL DEFAULT 'pending',  -- pending/confirmed/rejected
    created_at     TEXT DEFAULT (datetime('now'))
);

CREATE TABLE identities (
    id             INTEGER PRIMARY KEY,
    name           TEXT NOT NULL,
    reference_embs TEXT,  -- JSON: array of confirmed embedding IDs
    thumbnail_path TEXT,
    notes          TEXT,
    created_at     TEXT DEFAULT (datetime('now')),
    updated_at     TEXT DEFAULT (datetime('now'))
);

CREATE TABLE segments (
    id           INTEGER PRIMARY KEY,
    recording_id INTEGER NOT NULL,
    identity_id  INTEGER NOT NULL REFERENCES identities(id),
    start_ms     INTEGER NOT NULL,
    end_ms       INTEGER NOT NULL,
    confidence   REAL,
    exported     BOOLEAN DEFAULT FALSE,
    clip_path    TEXT,
    created_at   TEXT DEFAULT (datetime('now'))
);

-- Groups of identities (families, subgroups, etc.) — many-to-many
CREATE TABLE groups (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    notes      TEXT,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE group_members (
    group_id    INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    identity_id INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, identity_id)
);

-- Indexes
CREATE INDEX idx_frames_recording ON frames(recording_id, timestamp_ms);
CREATE INDEX idx_detections_frame ON detections(frame_id);
CREATE INDEX idx_detections_recording ON detections(recording_id, timestamp_ms);
CREATE INDEX idx_embeddings_detection ON embeddings(detection_id);
CREATE INDEX idx_tracks_recording ON tracks(recording_id);
CREATE INDEX idx_clusters_identity ON clusters(identity_id);
CREATE INDEX idx_segments_recording ON segments(recording_id);
CREATE INDEX idx_segments_identity ON segments(identity_id);
```

---

## Caching / Resumability

Carrying forward audio-archive patterns:

| Stage | Cache Key | Idempotency Check |
|-------|-----------|-------------------|
| Ingest | master file path | File exists at destination -> skip |
| Scout sample | recording_id + pass=scout | Frames exist for this recording+pass -> skip |
| Fill sample | recording_id + pass=fill | Same check |
| Detection | frame_id | `processed=TRUE` on frame -> skip |
| Embedding | detection_id | Embedding row exists -> skip |
| Tracking | recording_id | Tracks exist -> skip (or re-run with flag) |
| Clustering | recording_id | Clusters exist -> skip (or re-run with flag) |

**Job queue crash recovery:** Identical to audio-archive — `ResetStuckJobs()` on startup,
attempt tracking, backoff, cascade failure.

**Progress tracking:** Jobs store a `progress` JSON field. For long-running detection jobs
(thousands of frames), the worker updates progress periodically so the CLI can show
`detecting faces: 1200/3400 frames`.

---

## Library/Tool Recommendations

### Python ML Worker

| Purpose | Library |
|---------|---------|
| Face detection | `insightface` (RetinaFace/SCRFD) |
| Face embedding | `insightface` (ArcFace) |
| Face quality | `insightface` quality model |
| ML runtime | `onnxruntime` (CPU) or `onnxruntime-gpu` |
| HTTP API | `FastAPI` + `uvicorn` |
| Image handling | `opencv-python-headless` |
| Clustering | `hdbscan` or `sklearn.cluster.HDBSCAN` |
| Numpy | `numpy` |

`insightface` bundles RetinaFace detection + ArcFace embedding + face alignment in one
package. Eliminates 80% of the ML integration work.

### Go

| Purpose | Library |
|---------|---------|
| CLI | `cobra` |
| SQLite | `modernc.org/sqlite` |
| HTTP client | `net/http` (stdlib) |
| HTTP server | `net/http` (stdlib) |
| Templates | `html/template` (stdlib) |
| Interactivity | HTMX (JS, embedded) |
| Logging | `log/slog` (stdlib) |
| UUID | `google/uuid` |

### External Tools

| Tool | Purpose |
|------|---------|
| `ffmpeg` / `ffprobe` | Frame extraction, deinterlace, clip export, metadata |

---

## Performance Considerations

### Frame Sampling Budget

| Video Length | Uniform 0.5fps | Face-Present (~40%) | Adaptive Total |
|-------------|-----------------|---------------------|----------------|
| 1 hour | 1,800 | ~720 | ~3,240 with fill |
| 3 hours | 5,400 | ~2,160 | ~9,720 |
| 6 hours | 10,800 | ~4,320 | ~19,440 |

MVP (uniform 0.5fps) processes all frames. V2 adaptive cuts GPU work by ~50%.

### Batching

The ML worker accepts batch requests:

```
POST /detect  {"frame_paths": [...], "batch_size": 32}
POST /embed   {"crop_paths": [...], "batch_size": 64}
```

- **Detection:** 8-32 frames per batch (memory-bound, ~2GB for batch of 32)
- **Embedding:** 32-128 crops per batch (smaller images, less memory)
- **CPU fallback:** batch size 1-4

### GPU vs CPU

| Operation | CPU (M-series) | GPU (Metal) |
|-----------|----------------|-------------|
| RetinaFace detect | ~50ms/frame | ~10ms/frame |
| ArcFace embed | ~15ms/face | ~3ms/face |
| 1hr video (1,800 frames, 3 faces avg) | ~27 min detect, ~1.5 min embed | ~5 min detect, ~18s embed |

Start with CPU. M-series Macs are fast enough for MVP. Add CoreML/Metal provider in v2.

### Concurrency Model

```
Go main process
├── Frame extraction: 1 goroutine, FFmpeg subprocess (I/O bound)
├── ML dispatch: N goroutines sending batches to Python worker
│   └── Python worker: processes batches sequentially (GPU is the bottleneck)
├── DB writes: 1 goroutine consuming results channel (single-writer)
└── Job manager: dispatcher + worker pool (same as audio-archive)
```

**Pipeline parallelism:** While batch N is being detected by Python, Go extracts frames
for batch N+1. Detection and embedding can also pipeline.

### Disk I/O

- Frame JPEGs at 0.5fps for 1 hour: ~1,800 files x ~50KB = ~90MB
- Face crops: ~5,400 files (3 faces/frame avg) x ~5KB = ~27MB
- Embeddings: stored in SQLite as BLOBs, not individual files
- **Cleanup option:** Delete frame JPEGs after detection+embedding. Keep only face crops.

---

## Failure Modes and Robustness

| Failure | Impact | Mitigation |
|---------|--------|------------|
| Python worker crashes | Detection/embedding jobs fail | Job queue retries. Worker is stateless — restart picks up. Health check endpoint. |
| FFmpeg fails on corrupt video | Frame extraction incomplete | Log error with timestamp. Mark job as failed with partial progress. |
| Face detection returns 0 faces | Empty tracks/clusters | Expected for empty scenes. Flag if entire video has 0 detections. |
| Clustering produces bad groups | Wrong people merged | Conservative threshold + user review. Split UI in v2. |
| SQLite lock contention | Slow writes | `SetMaxOpenConns(1)`, WAL mode. |
| Out of disk space | Frame extraction fails | Check available space before starting. Fail early. |
| Model file missing/corrupt | Worker won't start | Checksum verification on startup. Clear error with download instructions. |
| VHS quality too poor | Detection misses faces | Lower confidence threshold. Log quality metrics. Let user adjust per-video. |
| Interrupted mid-detection | Partial results in DB | Frames table `processed` flag. Resume processes only unprocessed frames. |
| Very long video (6+ hours) | Job runs for hours | Progress reporting. Interruptible batches. Resume from last processed frame. |

### Subprocess Safety

```go
func applySubprocessSafety(cmd *exec.Cmd) {
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    cmd.WaitDelay = 30 * time.Second
    cmd.Cancel = func() error {
        return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
    }
}
```

Applied to all FFmpeg calls. Python worker gets graceful shutdown via context cancellation
on the HTTP client side.

---

## Python ML Worker API

```python
# worker/worker.py
from fastapi import FastAPI
from pydantic import BaseModel

app = FastAPI()

class DetectRequest(BaseModel):
    frame_paths: list[str]

class Detection(BaseModel):
    frame_path: str
    bbox: list[float]       # [x, y, w, h] normalized
    confidence: float
    landmarks: list[list[float]]  # 5 points, each [x, y]

class DetectResponse(BaseModel):
    detections: list[Detection]

class EmbedRequest(BaseModel):
    crop_paths: list[str]

class EmbeddingResult(BaseModel):
    crop_path: str
    vector: list[float]     # 512-dim, L2-normalized
    quality: float

class EmbedResponse(BaseModel):
    embeddings: list[EmbeddingResult]

class ClusterRequest(BaseModel):
    vectors: list[list[float]]
    min_cluster_size: int = 2

class ClusterResponse(BaseModel):
    labels: list[int]       # -1 = outlier

@app.get("/health")
def health():
    return {"status": "ok", "models_loaded": True}

@app.post("/detect", response_model=DetectResponse)
def detect(req: DetectRequest):
    """Detect faces in batch of frames. Returns all detections."""
    ...

@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    """Generate embeddings for batch of face crops."""
    ...

@app.post("/cluster", response_model=ClusterResponse)
def cluster(req: ClusterRequest):
    """Cluster embedding vectors via HDBSCAN."""
    ...
```

### Go Client Interface

```go
type Client struct {
    baseURL    string
    httpClient *http.Client
}

type DetectionResult struct {
    FramePath  string      `json:"frame_path"`
    Bbox       [4]float64  `json:"bbox"`
    Confidence float64     `json:"confidence"`
    Landmarks  [][2]float64 `json:"landmarks"`
}

type EmbeddingResult struct {
    CropPath string    `json:"crop_path"`
    Vector   []float64 `json:"vector"`
    Quality  float64   `json:"quality"`
}

func (c *Client) Detect(ctx context.Context, framePaths []string) ([]DetectionResult, error)
func (c *Client) Embed(ctx context.Context, cropPaths []string) ([]EmbeddingResult, error)
func (c *Client) Cluster(ctx context.Context, vectors [][]float64, minClusterSize int) ([]int, error)
func (c *Client) Health(ctx context.Context) error
```

---

## Core Pipeline Pseudocode

```go
// internal/pipeline/detect.go

func DetectFaces(ctx context.Context, cfg *config.Config, db *store.DB, ml *mlclient.Client, recordingID int64) error {
    frames, err := db.ListUnprocessedFrames(recordingID)
    if err != nil {
        return fmt.Errorf("list frames: %w", err)
    }
    if len(frames) == 0 {
        slog.Info("all frames already processed", "recording_id", recordingID)
        return nil
    }

    batchSize := cfg.DetectionBatchSize // default 16
    for i := 0; i < len(frames); i += batchSize {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        end := min(i+batchSize, len(frames))
        batch := frames[i:end]

        paths := make([]string, len(batch))
        for j, f := range batch {
            paths[j] = filepath.Join(cfg.DataDir, f.FramePath)
        }

        results, err := ml.Detect(ctx, paths)
        if err != nil {
            return fmt.Errorf("detect batch %d: %w", i/batchSize, err)
        }

        for _, det := range results {
            frame := findFrameByPath(batch, det.FramePath)

            cropPath, err := cropFace(cfg, frame, det)
            if err != nil {
                slog.Warn("crop failed", "frame_id", frame.ID, "err", err)
                continue
            }

            _, err = db.InsertDetection(store.Detection{
                FrameID:     frame.ID,
                RecordingID: recordingID,
                TimestampMs: frame.TimestampMs,
                BboxX:       det.Bbox[0],
                BboxY:       det.Bbox[1],
                BboxW:       det.Bbox[2],
                BboxH:       det.Bbox[3],
                Confidence:  det.Confidence,
                Landmarks:   marshalJSON(det.Landmarks),
                CropPath:    cropPath,
            })
            if err != nil {
                return fmt.Errorf("insert detection: %w", err)
            }
        }

        for _, f := range batch {
            db.MarkFrameProcessed(f.ID)
        }

        slog.Info("detection progress",
            "recording_id", recordingID,
            "done", min(end, len(frames)),
            "total", len(frames),
        )
    }

    return nil
}
```

```go
// internal/tracking/tracker.go

func BuildTracks(detections []store.Detection, embeddings map[int64][]float64, maxGapMs int64, simThreshold float64) []store.Track {
    sort.Slice(detections, func(i, j int) bool {
        return detections[i].TimestampMs < detections[j].TimestampMs
    })

    var tracks []store.Track
    var activeTracks []openTrack

    for _, det := range detections {
        emb := embeddings[det.ID]
        matched := false

        for i := range activeTracks {
            t := &activeTracks[i]
            gap := det.TimestampMs - t.lastMs

            if gap > maxGapMs {
                continue
            }

            sim := cosineSimilarity(emb, t.avgEmbedding)
            if sim > simThreshold {
                t.addDetection(det, emb)
                matched = true
                break
            }
        }

        if !matched {
            activeTracks = append(activeTracks, newOpenTrack(det, emb))
        }
    }

    for _, t := range activeTracks {
        tracks = append(tracks, t.finalize())
    }

    return tracks
}
```

---

## Project Folder Structure

```
video-archive/
├── cmd/va/main.go                    # CLI entrypoint
├── internal/
│   ├── cli/                          # Cobra commands
│   │   ├── root.go
│   │   ├── ingest.go
│   │   ├── sample.go
│   │   ├── detect.go
│   │   ├── embed.go
│   │   ├── track.go
│   │   ├── cluster.go
│   │   ├── review.go                 # starts HTTP server
│   │   ├── report.go
│   │   ├── export.go                 # clip export
│   │   ├── run.go                    # full pipeline
│   │   ├── worker.go                 # background job worker
│   │   ├── list.go
│   │   ├── show.go
│   │   ├── status.go
│   │   └── helpers.go
│   │
│   ├── pipeline/                     # Pure function orchestration
│   │   ├── ingest.go
│   │   ├── sample.go
│   │   ├── detect.go
│   │   ├── embed.go
│   │   ├── track.go
│   │   ├── cluster.go
│   │   ├── segment.go
│   │   └── export.go
│   │
│   ├── video/                        # FFmpeg/ffprobe wrappers
│   │   ├── probe.go
│   │   ├── frames.go                 # frame extraction
│   │   ├── clips.go                  # clip export
│   │   └── subprocess.go
│   │
│   ├── mlclient/                     # HTTP client for Python worker
│   │   ├── client.go
│   │   └── types.go
│   │
│   ├── tracking/                     # Face tracking logic (pure Go)
│   │   ├── tracker.go
│   │   └── merge.go
│   │
│   ├── review/                       # HTTP server for review UI
│   │   ├── server.go
│   │   ├── handlers.go
│   │   └── static/                   # embedded via go:embed
│   │       ├── htmx.min.js
│   │       └── style.css
│   │
│   ├── store/                        # SQLite persistence
│   │   ├── db.go
│   │   ├── migrations.go
│   │   ├── recordings.go
│   │   ├── jobs.go
│   │   ├── frames.go
│   │   ├── detections.go
│   │   ├── embeddings.go
│   │   ├── tracks.go
│   │   ├── clusters.go
│   │   ├── identities.go
│   │   └── segments.go
│   │
│   ├── jobs/                         # Async job orchestration
│   │   ├── manager.go
│   │   └── handlers.go
│   │
│   ├── config/
│   │   └── config.go
│   │
│   └── model/
│       └── types.go
│
├── worker/                           # Python ML worker
│   ├── pyproject.toml
│   ├── worker.py                     # FastAPI app
│   ├── detection.py                  # RetinaFace wrapper
│   ├── embedding.py                  # ArcFace wrapper
│   ├── clustering.py                 # HDBSCAN wrapper
│   └── models/
│       └── .gitkeep
│
├── templates/                        # html/template files (go:embed)
│   ├── layout.html
│   ├── review.html
│   ├── report.html
│   └── cluster_card.html
│
├── go.mod
├── go.sum
├── Makefile
├── README.md
└── DESIGN.md                         # this file
```

**Data directory** (`~/.video-archive/`):

```
~/.video-archive/
├── va.db                             # SQLite database
├── masters/YYYY/                     # original video files
├── frames/{recording_id}/            # extracted frame JPEGs
├── crops/{recording_id}/             # face crop JPEGs
├── thumbnails/                       # cluster representative thumbnails
├── clips/{recording_id}/             # exported video clips
├── models/                           # ML model weights (insightface)
└── logs/
```

---

## MVP Scope

**Goal:** Process one video file, detect all faces, cluster them, let user name them,
output timestamps.

| Component | MVP Scope |
|-----------|-----------|
| Ingest | Single file via CLI |
| Frame sampling | Uniform 0.5 fps (no adaptive passes) |
| ML worker | FastAPI with `/detect`, `/embed`, `/cluster` endpoints |
| Face detection | RetinaFace via insightface, batch endpoint |
| Face embedding | ArcFace via insightface, batch endpoint |
| Tracking | Group detections within N seconds with high embedding similarity |
| Clustering | HDBSCAN on track-level embeddings |
| Review UI | Basic web page: thumbnail grid, name input, merge button |
| Output | JSON report: person -> timestamps. CLI `show` command. |
| Database | Full schema from day 1 |
| Job queue | Full job queue from day 1 (pattern from audio-archive) |
| Clip export | Skip |
| Cross-video matching | Skip |
| Adaptive sampling | Skip |
| Watch/daemon mode | Skip |

**MVP CLI:**

```bash
va ingest video.mp4
va sample <id>          # extract frames at 0.5fps
va detect <id>          # face detection on all frames
va embed <id>           # embeddings for all detections
va track <id>           # link detections into tracks
va cluster <id>         # group tracks into identity clusters
va review <id>          # open browser for review UI
va report <id>          # output timestamps per named person

# Or all-in-one:
va run video.mp4        # ingest -> sample -> detect -> embed -> track -> cluster
va review <id>          # manual step
va report <id>          # after review
```

---

## V2 Roadmap

| Feature | Priority | Notes |
|---------|----------|-------|
| Adaptive sampling (scout/fill/track) | High | Biggest perf win for long videos |
| Clip export | High | Users will want this fast |
| Cross-video identity matching | High | Core value for archive use case |
| Batch ingest (`ingest-dir`) | Medium | Parallelize with audio-archive pattern |
| Watch mode + daemon | Medium | Auto-process new files |
| Split cluster UI | Medium | HDBSCAN sub-clustering |
| Scene detection | Medium | Improves tracking accuracy |
| Face quality filtering | Medium | Suppress bad embeddings pre-clustering |
| GPU acceleration (CoreML/Metal) | Medium | ONNX GPU provider for faster detection |
| Progress bars / TUI | Low | Nice UX for long jobs |
| Full-text search over identities | Low | "Show me all videos with Grandma" |
| Timeline visualization | Low | HTML timeline view of appearances |
| VHS-specific preprocessing | Low | Adaptive noise reduction, color correction |

---

## Build Order (Week 1)

**Days 1-2:** Project skeleton
- Go module, Cobra CLI, config, SQLite schema + migrations
- Copy and adapt job queue from audio-archive
- `ingest` command with ffprobe metadata extraction
- `sample` command — uniform 0.5fps frame extraction via FFmpeg

**Days 3-4:** Python ML worker
- FastAPI app with `/health`, `/detect`, `/embed` endpoints
- insightface model loading + inference
- Face cropping utility
- Go HTTP client for the worker

**Day 5:** Detection + embedding pipeline
- `detect` command — batch frames through worker, store results
- `embed` command — batch crops through worker, store embeddings
- End-to-end: ingest -> sample -> detect -> embed on a test video

**Days 6-7:** Tracking + clustering + review
- Face tracker (pure Go)
- Clustering via worker's `/cluster` endpoint
- Minimal review UI — thumbnail grid, name assignment
- `report` command — JSON output of person -> timestamps

## Intentionally Postponed

| Feature | Why |
|---------|-----|
| Adaptive sampling | Uniform 0.5fps works for MVP. Optimize after full pipeline validated. |
| Clip export | Timestamps are the core output. FFmpeg one-liners work ad-hoc. |
| Cross-video matching | Requires identity reference database. Build after single-video works. |
| GPU acceleration | CPU is fast enough on M-series for single videos. |
| Daemon/watch mode | Manual CLI is fine early on. Add after pipeline is stable. |
| VHS preprocessing | Deinterlacing covers the biggest issue. Advanced noise reduction can wait. |
| Split cluster UI | Merge is more common than split. Add when you hit bad clusters. |
| Timeline visualization | JSON report + review UI covers the need. |

---

# Post-MVP: Groups, Refinement, and Intelligent Merging

After the MVP proved out end-to-end (processing real home videos, naming people,
generating per-person timestamps, cross-video identity matching), the next set
of needs emerged from real archive use:

1. **Identities should refine across videos** — naming in one video helps match
   in all future videos (already working via cross-video matching, but can be sharpened)
2. **Groups/families** — query "every moment any Harker appears" across the archive
3. **Scene-aware merging** — avoid fragmented clips when the same scene has
   multiple people or multiple fragments of the same person
4. **Reviewable timestamps before clip export** — validate logical output
   before expensive clip generation

## Three-Level Merging Hierarchy

Think of segment merging as nested gap thresholds:

| Level | Default Gap | Purpose |
|-------|-------------|---------|
| **Within-person** | 5s | Merge fragmented segments for one person into continuous appearances (tracker breaks, face turned away, low sample rate) |
| **Within-scene** | 15-30s (tunable) | Unite different group members' overlapping appearances in the same scene |
| **Scene boundary** | > 30s gap | Distinct scenes, distinct clips |

Example for a "Harker Family" group:

- Raw tracks: Colton at 0:00-0:02, 0:04-0:06, 0:08-0:10 (track breaks during pan)
- Within-person merge (5s): Colton 0:00-0:10 (single continuous appearance)
- Alaina appears 0:05-0:20
- Within-scene merge (15s): Harker Family segment 0:00-0:20, tagged [Colton, Alaina]
- Later Alaina alone at 0:40-0:55, gap of 20s to previous segment
- Within-scene threshold crossed (assuming 15s): new scene, Harker Family 0:40-0:55 [Alaina]

## Data Model

Groups are a many-to-many layer on top of identities:

```
identities (existing)
  └─ Colton, Jessica, Lindsey, Alaina, Mom, Dad, Uncle Bob, ...

groups (NEW)
  └─ Harker Family, Pooles, Kids, Parents, Grandparents, ...

group_members (NEW) — many-to-many
  └─ (Harker Family, Colton), (Harker Family, Alaina), ...
  └─ (Parents, Mom), (Parents, Dad)
  └─ Mom can be in both "Harker Family" AND "Parents"
```

**Key decision:** Group segments are **computed on the fly** from existing
per-identity segments, not materialized to a new table. This keeps the source
of truth in `segments` and avoids sync issues when identities are renamed,
merged, or when members are added/removed from groups.

## New CLI Surface

### Group management (Phase 1)

```bash
va group create "Harker Family"
va group add "Harker Family" Colton Alaina Jessica Lindsey Mom Dad
va group list                          # all groups with member counts
va group members "Harker Family"       # list members
va group remove "Harker Family" Dad    # remove a member
va group delete "Harker Family"        # delete group (keeps identities)
```

### Group reporting (Phase 2 — the payoff)

```bash
va group-report "Harker Family"                # all videos, all appearances
va group-report "Harker Family" --json         # machine-readable
va group-report "Harker Family" --recording 3  # one video
va group-report "Harker Family" --scene-gap 20 # tune scene merge threshold
```

### Identity management (Phase 3)

```bash
va identity list                       # all known people with stats
va identity show Alaina                # per-video breakdown, total screen time
va identity merge "alaina" Alaina      # fix accidental duplicates
```

## Expected Output

```
Harker Family — appearances across 3 recordings

christmas-2005 (2005-12-25)
  0:00 - 3:04    (3m04s)  Colton, Alaina, Jessica, Lindsey
  3:20 - 4:10    (50s)    Colton, Alaina, Jessica, Lindsey
  5:28 - 7:30    (2m02s)  Alaina, Colton, Lindsey
  8:32 - 8:36    (4s)     Mom

lain-funny-faces (2002-01-01)
  0:00 - 2:38    (2m38s)  Alaina, Jessica
  3:42 - 4:58    (1m16s)  Alaina

Total: 6 scenes across 2 videos, 9m54s combined runtime
```

The user skims this, spots weird merges, tunes `--scene-gap`, re-runs. Only
after validation do clips get cut.

## Implementation Phases

### Phase 1: Groups schema + CRUD

- New tables: `groups`, `group_members` (with CASCADE delete)
- Store methods: `CreateGroup`, `DeleteGroup`, `ListGroups`, `AddMember`,
  `RemoveMember`, `ListMembers`, `FindGroupByName`
- CLI commands: `va group create/list/add/remove/members/delete`
- Identity lookup by name (case-insensitive, prefix match) for ergonomic CLI

### Phase 2: Three-level merging + group-report

- Refine `GenerateSegments` to tune within-person gap (5s default, was 3s)
- New pipeline function `GenerateGroupReport(groupID, sceneGapMs)`:
  1. Load all members of the group
  2. For each recording, collect every segment from any member
  3. Merge overlapping/nearby segments using `sceneGapMs` (scene-level merge)
  4. Attribute each output segment with the set of members present
  5. Return grouped by recording with totals
- CLI: `va group-report <name>` with `--json`, `--recording`, `--scene-gap` flags
- Human-readable output shows member attribution per segment

### Phase 3 (later): Identity refinement tools

- `va identity list` — appearance counts across the archive
- `va identity show <name>` — per-video breakdown
- `va identity merge <from> <into>` — combine accidentally-split identities;
  reassign all clusters, delete the now-unused identity, regenerate segments
- Quality-weighted reference embeddings — high-confidence tracks count more
  during cross-video matching
- `--dry-run` mode for destructive identity operations

### Phase 4 (optional): Shot-based scene detection

- Replace time-gap heuristic for scene merging with FFmpeg `scdet` shot detection
- Pre-extract shot boundaries per recording; merge segments only within the
  same shot
- More accurate than pure time-gap, especially for fast-cut videos

## Non-destructive by Design

None of this replaces existing data — groups sit on top as a new view layer.
Existing `identities`, `clusters`, and `segments` tables are untouched.
Deleting a group has no effect on the identities or their segments.

---

# As-Built Appendix (updated 2026-04-15)

Pointers to design decisions made after the original MVP + groups work, recorded here
so future-you can resume without re-deriving them.

## Ingest link modes

`pipeline.IngestFile` supports three placement modes via `IngestOptions.LinkMode`:

| Mode | CLI flag | Behaviour | Use when |
|------|----------|-----------|----------|
| `LinkModeCopy` (default) | *(none)* | Full byte copy into `masters/YYYY/` | Small files, or when you want an independent archive copy |
| `LinkModeHardlink` | `--link` | `os.Link` into `masters/YYYY/` | Large files, same filesystem as source. Zero extra disk. Source deletion keeps archive intact. **Modifications to the source are visible in the archive** (same inode) |
| `LinkModeSymlink` | `--symlink` | `os.Symlink` into `masters/YYYY/` | Cross-filesystem case. **Breaks if the source file moves or is deleted.** |

Flags are mutually exclusive (cobra `MarkFlagsMutuallyExclusive`) and wired into
`va ingest`, `va ingest-dir`, and `va run`. Hardlink is the right default for the
big-file archive (the ~73 GB home-video collection on `~/Desktop/Home Videos`).

### Undated recordings

`pipeline.IngestFile` allows empty dates (stored as empty in the `date` column).
Files without a recoverable date from the filename land under
`masters/unknown/` instead of getting stamped with today's date. The review UI
handles empty dates gracefully (displays `—`).

## Frame dimensions in DB

`frames.width` / `frames.height` are populated at insert time (via `image.DecodeConfig`
in `pipeline/sample.go`). For archives that predate this fix, run:

```
va backfill-frames
```

Iterates rows with `width = 0 OR NULL`, reads the JPEG header for each, updates. Safe
to re-run; idempotent. Note that the review UI's lightbox does not depend on these
fields (reads `img.naturalWidth/Height`), but downstream code that joins frames with
recordings expects real values.

## CLI surface (current)

Beyond what's in the earlier sections:

- `va review` (no arg) — opens the home page instead of requiring a recording ID.
  `va review {id}` still works to land directly on that recording's review page.
- `va backfill-frames` — populate missing frame dims (see above).
- `va ingest` / `va run` — added `--link` / `--symlink`.
- `va ingest-dir <dir>` — recursive batch ingest with filename-based date
  extraction (`YYYY-MM-DD`, `YYYY_MM_DD`, or bare 19xx/20xx year). Supports
  `--dry-run` to preview. See the Scaling section below.
- `va run-all` — runs the full pipeline on every unprocessed recording,
  shortest first. Idempotent — re-running skips already-done stages.
  `--limit N`, `--skip-match`, `--dry-run` flags available.
- `va scenes <id>` / `va scene-map <id>` — shot-boundary detection and
  face-informed scene merging + people attribution.

## Browser-video compatibility

The sticky player on `/review/{id}` serves the master file at `/static/master/...` but
only exposes it for these container extensions: `.mp4`, `.m4v`, `.mov`, `.webm`. Other
formats (`.mkv`, `.avi`, `.wmv`) hit a graceful "unavailable" notice — crop-click still
opens the lightbox, but there's no seek-in-video. If/when the archive accumulates
unplayable formats, the right move is a remux-on-ingest step (probably `-c copy` into
mp4 where the codec allows, full re-encode otherwise). Currently the 43-file
home-video collection is all mov/mp4, so this hasn't come up.

## Known quirks / deferred work

- **Crop thumbnails on identity detail page use `cluster.thumbnail_path` only** — no
  detection-level thumbnail traversal like the per-recording review page. Good enough
  for an overview; upgrade if it feels sparse.
- **Cluster split UI** still deferred (per original design).

## Review UI keyboard shortcuts

The per-recording review page (`/review/{id}`) supports keyboard navigation:

| Key | Action |
|-----|--------|
| `j` / `↓` | Select next cluster card |
| `k` / `↑` | Select previous cluster card |
| `n` | Focus the selected cluster's name input |
| `Enter` | Submit the name (standard form behaviour while focused) |
| `r` | Reject the selected cluster (native `confirm()` prompt) |
| `Esc` | Close lightbox/help overlay, or blur the current input |
| `?` | Toggle the keyboard-shortcut help overlay |

Shortcuts are suppressed while an `input`/`select`/`textarea` has focus, so typing a
name never triggers `r`/`n`/etc. The currently selected card gets a subtle purple
outline. First non-rejected card is selected on page load.

## Auto-regenerated segments

All cluster/identity mutation handlers call `pipeline.GenerateSegments` for affected
recordings as part of the request lifecycle — no more manual `va segments {id}` after
rename / merge / detach / reject / name flows. The CLI command still exists for bulk
re-runs (e.g., after tuning `GapThresholdMs`).

Identity-level mutations (`merge`, `delete`, and rename-that-merges) look up
`ListRecordingIDsForIdentity` **before** the destructive op so all recordings that
had clusters *or* segments referencing that identity get regenerated. Cluster-level
mutations only regenerate the single recording they touch.

Regen errors are logged but never fail the HTTP request — segments are a derived
view, and a stale view is preferable to a broken action.

## Rename-collision confirmation

`POST /identities/{id}/rename` with a name that case-insensitively matches a
*different* existing identity now redirects (`HX-Redirect` for HTMX) to
`GET /identities/{id}/rename-confirm?name=<new>`, which shows a confirmation page
describing what would happen. The confirm form re-POSTs to `/rename` with
`confirm=1`, which is the only path that actually performs the merge.

If the collision disappears between the initial submit and the confirm (another
tab deleted or renamed the target), the confirm handler bounces back to
`/identities` rather than rendering a stale prompt.

## Template sanity tests

`internal/review/templates_test.go` parses and executes every inline template with
zero-value data. Catches field/func-name drift before render time. Not a correctness
test, just a "doesn't blow up." Worth keeping in sync if templates get refactored.

---

# Next Evolution: Scene-Aware Retrieval

## North Star

The archive's retrieval unit should be a **scene**, not a segment or a timestamp.
A scene is a continuous shot (or group of tightly related shots) from the original
video. When a user searches for a person, they get back whole scenes — "the
Christmas morning unwrapping scene" — not "37 one-second face detections."

The existing pipeline already detects, tracks, clusters, and names faces. Segments
merge track fragments into per-person time ranges. What's missing is the top-down
scene structure that frames those appearances in context.

### Shift from segments to scenes

| Before (segments) | After (scenes) |
|-------------------|----------------|
| Bottom-up: merge track time ranges with gap threshold | Top-down: detect shot boundaries first, then map people into shots |
| Retrieval unit is a per-person segment | Retrieval unit is a multi-person scene |
| "Colton appears at 1:02–1:14, 1:30–1:45" | "Christmas scene 0:45–2:10: Colton, Alaina, Mom" |
| Gap-threshold merging is sensitive to sampling rate | Shot detection uses visual features, independent of sampling |

Segments remain as an intermediate layer (tracks → segments → scene attribution),
but the scene becomes the user-facing output.

## Scene Detection

### Approach: FFmpeg `scdet` filter with VHS preprocessing

```bash
ffmpeg -i source.mp4 -vf "yadif=mode=1,hqdn3d=4:3:6:4.5,scdet=threshold=10" -f null - 2>&1
# Output per detected boundary:
# [Parsed_scdet_0 @ ...] lavfi.scd.score: 15.210, lavfi.scd.time: 193.993
```

Why `scdet` over Python `scenedetect`:
- FFmpeg is already a project dependency (no new toolchain)
- Runs at 120x+ speed on VHS content (2 seconds per 5-minute video)
- Threshold tunable per-recording via CLI flag

### VHS-specific preprocessing (critical)

`scdet` computes SAD (sum of absolute differences) between **consecutive luma
frames**. On raw interlaced VHS this produces garbage because:

1. Every other frame flips field rows → huge spurious SAD between frames that
   actually show the same content
2. VHS noise is per-frame random → every consecutive pair differs by something,
   raising the noise floor enough to swamp real scene-change signals

The filter chain solves both:

- **`yadif=mode=1`** deinterlaces into progressive frames. Removes the
  field-flipping artifact entirely.
- **`hqdn3d=4:3:6:4.5`** spatial+temporal denoiser. Temporally averages
  pixel values across adjacent frames *unless* motion is detected. Noise
  inside a continuous shot collapses; real cuts still produce SAD spikes
  because motion breaks the temporal average.
- **`scdet=threshold=10`** is FFmpeg's documented default and works correctly
  once the preceding filters clean the signal.

Earlier iterations shipped `threshold=5` with no preprocessing — that's below
the noise floor of raw VHS and produced 10–100× more boundaries than real
scene changes warranted. Do not lower the threshold without also removing
the denoise prefix.

### Measured impact of preprocessing

Re-detection on the home-video test archive with the tuned filter chain:

| Recording | Before (t=5, no prep) | After (t=10, yadif+hqdn3d) |
|-----------|----------------------|---------------------------|
| colton-being-colton (4m12s) | 106 raw boundaries → 17 scenes | 8 raw boundaries → 7 scenes |
| christmas-2005 (8m58s) | 14 raw boundaries → 11 scenes | 6 raw boundaries → 7 scenes → **3 after face-merge** |
| lain-funny-faces (5m4s) | 6 boundaries → 6 scenes | 0 boundaries → 1 scene directly |
| barbie-dance (3m4s) | 3 boundaries → 4 scenes | 0 boundaries → 1 scene |

The dramatic drop in raw boundaries confirms the noise hypothesis: the earlier
pipeline was detecting VHS noise, not content changes.

Default: **10.0** for the archive. Configurable via `va scenes --threshold`.

### Post-processing rules

1. **Merge nearby boundaries:** If two boundaries are within 1 second, keep only
   the one with the higher score. VHS transitions sometimes produce a burst of
   2–3 detections for a single cut.
2. **Minimum scene duration:** Scenes shorter than `--min-duration` (default 3s)
   are absorbed into the previous scene. Catches flicker/noise.
3. **Implicit first/last scene:** Frame 0 is always a scene start. Video end is
   always a scene end.

### Data model

```sql
CREATE TABLE scenes (
    id            INTEGER PRIMARY KEY,
    recording_id  INTEGER NOT NULL REFERENCES recordings(id),
    start_ms      INTEGER NOT NULL,
    end_ms        INTEGER NOT NULL,
    score         REAL,    -- detection score at the boundary that started this scene
    created_at    TEXT DEFAULT (datetime('now')),
    UNIQUE(recording_id, start_ms)
);

CREATE TABLE scene_people (
    scene_id          INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    identity_id       INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    first_appearance_ms INTEGER NOT NULL,  -- relative to video start (not scene start)
    total_time_ms     INTEGER NOT NULL,    -- how long this person appears in this scene
    PRIMARY KEY (scene_id, identity_id)
);

CREATE INDEX idx_scenes_recording ON scenes(recording_id, start_ms);
CREATE INDEX idx_scene_people_identity ON scene_people(identity_id);
```

### Pipeline integration

Scene detection is independent of face detection and can run in parallel:

```
va ingest → va sample ─────→ va detect → va embed → va track → va cluster
          └→ va scenes ──────────────────────────────────────────────┘
                                                                     ↓
                                                          va scene-map
```

`va scenes <id>` — detect shot boundaries, store in `scenes` table.
`va scene-map <id>` — cross-reference scenes with segments to populate `scene_people`.

Alternatively, `va run` includes both as part of the full pipeline.

## Scene-People Mapping

After both scenes and segments exist for a recording, `scene-map` populates the
`scene_people` join table:

```
For each scene S:
  For each segment SEG where SEG.start_ms < S.end_ms AND SEG.end_ms > S.start_ms:
    overlap_start = max(S.start_ms, SEG.start_ms)
    overlap_end   = min(S.end_ms, SEG.end_ms)
    overlap_ms    = overlap_end - overlap_start
    if overlap_ms < 500ms: continue            # reject boundary-straddle artifacts
    first_ms      = overlap_start
    → INSERT scene_people(S.id, SEG.identity_id, first_ms, overlap_ms)
```

If a person appears in multiple segments within the same scene (e.g. tracker
lost and re-acquired), their `total_time_ms` accumulates and `first_appearance_ms`
takes the earliest.

Scene-people is regenerated from scratch each time (delete + reinsert), same as
segments. It's a derived view.

### The 500ms minimum overlap (boundary-straddle fix)

Segments are merged bottom-up with a 5-second within-person gap threshold,
which means a segment's start/end times can trail the last detection by up
to a few seconds. When a real scene boundary falls inside this trailing
slack, the segment straddles both sides.

Example from the archive: a sunset scene (empty, no faces) was followed
by a Mom-on-phone scene starting at `8:32.28`. Mom's segment was
`8:32.000–8:36.000` — straddling the boundary by 278 ms on the sunset side.
Any-overlap attribution put Mom in *both* scenes, which then:

1. Polluted the UI: the sunset showed "Mom" as a present person
2. Triggered a false face-merge: adjacent scenes "both had Mom" → merged

Requiring `overlap ≥ 500 ms` (`MinScenePeopleOverlapMs` in `pipeline/scene_map.go`
and `SceneMergeOptions.MinSegmentOverlapMs` in `pipeline/scene_merge.go`)
rejects these artifacts while preserving genuine brief appearances.

## Face-informed scene merging

`MergeScenesByPeople` (`pipeline/scene_merge.go`) is a post-processing pass
that catches over-segmentation where `scdet` picks up spurious boundaries
inside a continuous shot (camera wobble, pan, VHS noise bursts).

**Rule:** merge two adjacent scenes if:
- Both have at least one attributed person (empty scenes are barriers)
- Their identity sets have Jaccard similarity ≥ 0.5 (default, configurable)
- Segment attribution uses the 500 ms minimum described above

Segment attribution is computed locally (not read from `scene_people`) so
the merger can run before `scene-map`. The merge rewrites the `scenes`
table; `scene-map` then runs on the merged scenes.

Tunable via `va scene-map --overlap 0.5` and `--no-merge` to disable.

## Search + Ranking (future)

### Query model

```
Search(people: [identityID...], groups: [groupID...], mode: AND|OR)
  → returns: [Scene] ranked by relevance
```

- **OR mode** (default): any scene containing at least one queried person
- **AND mode**: scenes where all queried people appear

### Ranking criteria

| Factor | Weight | Rationale |
|--------|--------|-----------|
| Total screen time of queried people | High | Longer appearances are more meaningful |
| Co-occurrence count | High | Scenes where multiple queried people appear together |
| First appearance proximity to scene start | Medium | Person visible from the beginning = more "about" them |
| Scene duration | Low tiebreaker | Prefer longer scenes (more context) |

Scoring is a weighted sum computed in Go, not in SQL. SQL fetches candidate
scenes; Go ranks them.

### Search results UI

Each result is a **scene card**:
- Recording name + date
- Scene time range + duration
- People present (with per-person screen time)
- Thumbnail (first frame of scene, or best-quality face crop from scene)
- "Play full scene" → seek video to scene start
- "Jump to first appearance" → seek to the queried person's first_appearance_ms

---

# Angular Frontend (Learning Project)

The existing HTMX review UI continues to work. The Angular frontend is a separate
SPA that talks to JSON API endpoints on the Go server. Both frontends coexist.

## Architecture

```
┌──────────────────┐       JSON API        ┌──────────────────────┐
│  Angular SPA     │  ←──────────────────→  │  Go HTTP Server      │
│  localhost:4200   │                        │  localhost:8080       │
│  (ng serve)       │                        │  /api/...  (JSON)    │
│                   │                        │  /review/  (HTMX)    │
│                   │                        │  /static/  (files)   │
└──────────────────┘                        └──────────────────────┘
```

In production (single-binary): Angular builds to `dist/`, Go embeds it via
`go:embed` and serves at `/app/`. No Node runtime needed.

## Planned views

| View | Purpose |
|------|---------|
| **Library** | Grid/list of all recordings with status pills (processed, scenes detected, reviewed) |
| **Cluster Review** | Angular port of existing review page — richer interaction, keyboard-first |
| **People** | Identity grid with filtering, group assignment |
| **Groups** | Group management + group report viewer |
| **Search** | Person/group multi-select → ranked scene results |
| **Scene Player** | Video player with scene scrubber, person timeline overlay, scene navigation |

## JSON API endpoints (to be added)

```
GET  /api/recordings
GET  /api/recordings/{id}
GET  /api/recordings/{id}/clusters
GET  /api/recordings/{id}/scenes
GET  /api/identities
GET  /api/identities/{id}
GET  /api/groups
GET  /api/search?people=1,2&groups=3&mode=and
POST /api/clusters/{id}/name
POST /api/clusters/{id}/reject
POST /api/clusters/merge
POST /api/identities/{id}/rename
POST /api/identities/merge
```

Each JSON endpoint reuses the same store methods the HTMX handlers use.
No business logic duplication.

---

# Future Features (post-scene detection)

| Feature | Value | Approach |
|---------|-------|----------|
| **Speech-to-text** | Identify context (names, events), enable text search | Whisper (same as audio-archive) on extracted audio tracks. Store transcripts per scene. |
| **Scene summaries** | "Christmas morning", "birthday party" | CLIP embeddings of scene keyframes → label via LLM or manual tagging |
| **Semantic search** | "find clips where Mom is speaking" | Combine STT transcripts + face presence + CLIP embeddings |
| **SAM2 segmentation** | Pixel-precise face/person tracking for manual correction | Heavy GPU requirement. Useful for edge cases where bbox tracking fails |
| **Clip export** | Cut scene clips per person or group | FFmpeg + scene boundaries. Mostly plumbing, deferred until scene pipeline validated |
| **Timeline visualization** | Visual timeline of who appears when | Angular component over scene_people data. Natural fit for the SPA |

---

# Scaling to the Full Archive

The test pass validated the pipeline on 5 recordings. The real target is the
home-video collection at `~/Desktop/Home Videos`.

## Scope

| Metric | Value |
|--------|-------|
| File count | 43 videos |
| Total duration | ~18 hours |
| Disk footprint | 73 GB (hardlinked, no duplication into archive) |
| File size range | ~20 MB (1-min clips) to ~20 GB (multi-hour "Family LONG" tapes) |
| Container formats | mostly `.mov` and `.mp4` (both browser-playable) |

## Expected processing time (CPU)

Based on measured throughput on the test recordings:

| Stage | Per hour of video | For 18 hours |
|-------|-------------------|--------------|
| Sampling (ffmpeg, 0.5 fps) | ~1 min | ~18 min |
| Scene detection (yadif+hqdn3d+scdet) | ~30 s | ~9 min |
| Face detection (RetinaFace, CPU) | ~27 min | **~8 hours** |
| Embedding (ArcFace, CPU) | ~1.5 min | ~27 min |
| Tracking/clustering/matching | <1 min | trivial |
| **Total wallclock** | — | **~8–10 hours** |

Detection dominates. GPU acceleration (CoreML/Metal provider) would cut this
to ~1–2 hours but is deferred (see V2 roadmap).

Expected derived-data growth: ~2–3 GB (frames + crops). Database: MB range.

## Cross-video identity matching mechanics

From `pipeline/match.go`: when a new recording finishes clustering, pending
clusters are compared against **reference embeddings** derived from all
confirmed clusters across the archive. If cosine similarity ≥ threshold
(default 0.5), the cluster is auto-labeled.

This means: **naming clusters in one video improves labeling in future videos.**
The clustering *within* a video is unchanged (HDBSCAN runs fresh per recording),
but the *labeling* of those clusters bootstraps from prior knowledge.

After ~5 well-named examples of a person, new appearances auto-match without
human review. Review load drops exponentially after the first handful of videos.

### Max-over-refs matching

Reference embeddings are stored **individually** per confirmed cluster, and
matching uses **max cosine similarity over the full reference set** — not the
average. This matters for two reasons:

1. **Age progression:** A single person (baby → teen → adult) produces very
   different embeddings at different ages. ArcFace is invariant to pose,
   lighting, and expression but *not age*. Averaging a baby centroid and an
   adult centroid produces a midpoint vector that matches neither well. Max
   similarity lets a new baby appearance match the baby references strongly
   even under the same merged identity as the adult.
2. **Pose/lighting diversity:** Even within one age range, different poses
   (profile, three-quarter, front) and lighting conditions produce embeddings
   that aren't linearly averageable. Max-over-refs is robust to this.

Earlier iterations averaged the refs — that worked on 5 test videos where
everyone was roughly the same age but would have failed on the multi-decade
archive.

## The age progression problem (long-lived archives)

The archive spans ~20 years of Harker-family footage. People who appear as
babies in 2002 tapes are adults in 2020s tapes. Without care this produces
separate identity records per age bucket and queries like "every appearance
of Colton" only return one era.

**Handling strategy:**

1. Process all videos first — cross-video matching will auto-group clusters
   that are close in time (same age).
2. Manually merge identity records that represent the same person across
   ages via the `/identities/merge` review UI. `MergeIdentities` reassigns
   all clusters and segments, and — with max-over-refs matching — future
   video processing benefits from the expanded reference set without
   dilution.
3. Groups still work as a fallback: put "Colton (child)" and "Colton (teen)"
   into group "Colton (all ages)" if you prefer not to merge.

## Sibling / family-resemblance confusion

Family members often have similar features. The default auto-match threshold
(0.5) can misattribute a sibling's cluster to another sibling. Mitigations:

- **Raise `AutoApply` threshold to 0.6** for the batch pass — forces more
  clusters to manual review but reduces false auto-matches on siblings.
- **Review unmatched clusters liberally** — the first few videos with each
  family member should be reviewed carefully to build accurate reference
  embeddings.

## Batch processing commands

Two new CLI commands for archive-scale operations:

**`va ingest-dir <directory> [--link] [--pattern glob]`**
- Recursively scans for video files
- Extracts date from filename (e.g., `2002-01-01_*` or `2005 - ...`)
- Calls `pipeline.IngestFile` per file (idempotent — skips already-ingested)
- `--link` hardlinks into `masters/` (recommended for the big archive)

**`va run-all [--limit N] [--skip-ml]`**
- Iterates recordings that haven't been fully processed
- Runs the full pipeline on each (sample → scenes → detect → embed → track → cluster → match → segments → scene-map)
- Each stage is idempotent, so interruption/resume works
- Logs progress per recording

Both commands preserve the single-file commands (`va ingest`, `va run`) for
spot-processing.

## Recommended rollout

1. **Fix matching algorithm** (max-over-refs, described above). Prerequisite.
2. **Build `ingest-dir` + `run-all`**.
3. **Ingest everything** — minutes, hardlinked.
4. **First wave: process ~5 diverse videos** (different years, different
   people). ~1 hour CPU. Review and name carefully — these become reference
   embeddings for the rest.
5. **Background-process remaining 38 videos** with `caffeinate -dis
   va run-all`. ~7–9 hours overnight.
6. **Final review pass** — most clusters auto-matched from the wave-1
   references. Review unmatched.

Total: a day of wallclock, ~2–3 hours of active human attention concentrated
in the first naming pass.

## Operational gotchas

| Gotcha | Mitigation |
|--------|------------|
| Mac sleeps during long runs | `caffeinate -dis` prefix keeps display+system awake |
| Huge files (20 GB) dominate wallclock | Process smaller files first for faster feedback |
| Case-inconsistent names ("Colton" vs "colton") pile up | `FindIdentityByNameCaseInsensitive` is used on rename; but review existing DB for duplicates before batch processing |
| Low-quality VHS produces garbage clusters | Reject liberally. The 0.5 detection-confidence floor already filters a lot |
| Review UI is per-video, not global | Known limitation. A cross-video unnamed-cluster view would help; deferred |
| Filename dates are inconsistent | `ingest-dir` extracts where possible; falls back to empty date (recording still works) |
