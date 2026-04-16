package pipeline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// DetectResult describes the outcome of face detection.
type DetectResult struct {
	FacesFound   int
	FramesDone   int
	FramesTotal  int
	Skipped      bool
}

// DetectFaces runs face detection + embedding on all unprocessed frames.
// The ML worker returns both detections and embeddings in one pass.
func DetectFaces(ctx context.Context, cfg config.Config, db *store.DB, ml *mlclient.Client, recordingID int64) (*DetectResult, error) {
	frames, err := db.ListUnprocessedFrames(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing frames: %w", err)
	}
	if len(frames) == 0 {
		slog.Info("all frames already processed", "recording_id", recordingID)
		return &DetectResult{Skipped: true}, nil
	}

	// Ensure crops directory exists
	cropsDir := filepath.Join(cfg.CropsDir(), fmt.Sprintf("%d", recordingID))
	if err := os.MkdirAll(cropsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating crops dir: %w", err)
	}

	batchSize := 16
	totalFaces := 0

	for i := 0; i < len(frames); i += batchSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		end := i + batchSize
		if end > len(frames) {
			end = len(frames)
		}
		batch := frames[i:end]

		// Build absolute paths for the ML worker
		paths := make([]string, len(batch))
		for j, f := range batch {
			paths[j] = filepath.Join(cfg.DataDir, f.FramePath)
		}

		// Call ML worker
		result, err := ml.Detect(ctx, paths)
		if err != nil {
			return nil, fmt.Errorf("detect batch starting at frame %d: %w", i, err)
		}

		// Process each detection
		// Track face index per frame to avoid crop filename collisions
		faceIdx := make(map[int64]int) // frame ID -> count
		for _, det := range result.Detections {
			// Find the corresponding frame
			frame := findFrameByPath(batch, det.FramePath, cfg.DataDir)
			if frame == nil {
				slog.Warn("detection for unknown frame path", "path", det.FramePath)
				continue
			}

			// Crop the face from the original frame
			idx := faceIdx[frame.ID]
			faceIdx[frame.ID] = idx + 1
			cropPath, err := cropFaceN(cfg, cropsDir, frame, det, idx)
			if err != nil {
				slog.Warn("failed to crop face", "frame_id", frame.ID, "error", err)
				continue
			}

			// Convert landmarks to JSON
			landmarksJSON, _ := json.Marshal(det.Landmarks)

			// Store detection
			relCropPath, _ := filepath.Rel(cfg.DataDir, cropPath)
			detID, err := db.InsertDetection(&model.Detection{
				FrameID:     frame.ID,
				RecordingID: recordingID,
				TimestampMs: frame.TimestampMs,
				BboxX:       det.Bbox[0],
				BboxY:       det.Bbox[1],
				BboxW:       det.Bbox[2] - det.Bbox[0], // x2-x1 = width
				BboxH:       det.Bbox[3] - det.Bbox[1], // y2-y1 = height
				Confidence:  det.Confidence,
				Landmarks:   string(landmarksJSON),
				CropPath:    relCropPath,
			})
			if err != nil {
				return nil, fmt.Errorf("inserting detection: %w", err)
			}

			// Store embedding if present
			if len(det.Embedding) > 0 {
				vectorBlob := float64sToBytes(det.Embedding)
				err = db.InsertEmbedding(&model.Embedding{
					DetectionID: detID,
					RecordingID: recordingID,
					Vector:      vectorBlob,
					ModelUsed:   "insightface-buffalo_l",
				})
				if err != nil {
					return nil, fmt.Errorf("inserting embedding: %w", err)
				}
			}

			totalFaces++
		}

		// Mark frames as processed
		for _, f := range batch {
			db.MarkFrameProcessed(f.ID)
		}

		slog.Info("detection progress",
			"recording_id", recordingID,
			"frames_done", min(end, len(frames)),
			"frames_total", len(frames),
			"faces_this_batch", len(result.Detections),
		)
	}

	slog.Info("detection complete",
		"recording_id", recordingID,
		"total_faces", totalFaces,
		"total_frames", len(frames),
	)

	return &DetectResult{
		FacesFound:  totalFaces,
		FramesDone:  len(frames),
		FramesTotal: len(frames),
	}, nil
}

// findFrameByPath locates a frame in the batch by matching the absolute path.
func findFrameByPath(batch []model.FrameSample, absPath, dataDir string) *model.FrameSample {
	for i := range batch {
		fullPath := filepath.Join(dataDir, batch[i].FramePath)
		if fullPath == absPath {
			return &batch[i]
		}
	}
	return nil
}

// cropFaceN extracts the face region from a frame image and saves it as a JPEG.
// The idx parameter disambiguates multiple faces within the same frame.
func cropFaceN(cfg config.Config, cropsDir string, frame *model.FrameSample, det mlclient.FaceDetection, idx int) (string, error) {
	framePath := filepath.Join(cfg.DataDir, frame.FramePath)

	f, err := os.Open(framePath)
	if err != nil {
		return "", fmt.Errorf("opening frame: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decoding frame: %w", err)
	}

	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	// bbox is [x1, y1, x2, y2] in pixels — add 20% padding
	x1 := det.Bbox[0]
	y1 := det.Bbox[1]
	x2 := det.Bbox[2]
	y2 := det.Bbox[3]
	w := x2 - x1
	h := y2 - y1
	padX := w * 0.2
	padY := h * 0.2

	cropRect := image.Rect(
		int(math.Max(0, x1-padX)),
		int(math.Max(0, y1-padY)),
		int(math.Min(imgW, x2+padX)),
		int(math.Min(imgH, y2+padY)),
	)

	// SubImage if the underlying type supports it
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	si, ok := img.(subImager)
	if !ok {
		return "", fmt.Errorf("image type %T does not support SubImage", img)
	}
	cropped := si.SubImage(cropRect)

	// Save crop
	cropName := fmt.Sprintf("det_%d_%012d_%d.jpg", frame.ID, frame.TimestampMs, idx)
	cropPath := filepath.Join(cropsDir, cropName)

	out, err := os.Create(cropPath)
	if err != nil {
		return "", fmt.Errorf("creating crop file: %w", err)
	}
	defer out.Close()

	if err := jpeg.Encode(out, cropped, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("encoding crop: %w", err)
	}

	return cropPath, nil
}

// float64sToBytes converts a slice of float64 to a byte slice (little-endian).
func float64sToBytes(vals []float64) []byte {
	buf := make([]byte, len(vals)*8)
	for i, v := range vals {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	return buf
}

// bytesToFloat64s converts a byte slice back to float64 slice.
func bytesToFloat64s(data []byte) []float64 {
	n := len(data) / 8
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		vals[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
	}
	return vals
}
