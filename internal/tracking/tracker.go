package tracking

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"

	"github.com/colton/video-archive/internal/model"
)

// Config controls tracking behavior.
type Config struct {
	MaxGapMs     int64   // maximum gap between detections to continue a track
	SimThreshold float64 // minimum cosine similarity to match embedding to track
}

// DefaultConfig returns sensible defaults for VHS footage at 0.5 fps sampling.
func DefaultConfig() Config {
	return Config{
		MaxGapMs:     5000,  // 5 seconds — generous for low sample rate
		SimThreshold: 0.55,  // lower than ideal to handle VHS quality variation
	}
}

// openTrack is a track being built.
type openTrack struct {
	detectionIDs []int64
	embeddings   [][]float64
	avgEmbedding []float64
	startMs      int64
	lastMs       int64
	totalConf    float64
}

func newOpenTrack(det model.Detection, emb []float64) openTrack {
	return openTrack{
		detectionIDs: []int64{det.ID},
		embeddings:   [][]float64{emb},
		avgEmbedding: append([]float64{}, emb...),
		startMs:      det.TimestampMs,
		lastMs:       det.TimestampMs,
		totalConf:    det.Confidence,
	}
}

func (t *openTrack) addDetection(det model.Detection, emb []float64) {
	t.detectionIDs = append(t.detectionIDs, det.ID)
	t.embeddings = append(t.embeddings, emb)
	t.lastMs = det.TimestampMs
	t.totalConf += det.Confidence

	// Update running average embedding
	n := float64(len(t.embeddings))
	for i := range t.avgEmbedding {
		t.avgEmbedding[i] = t.avgEmbedding[i]*(n-1)/n + emb[i]/n
	}
}

func (t *openTrack) finalize(recordingID int64) model.Track {
	idsJSON, _ := json.Marshal(t.detectionIDs)
	return model.Track{
		RecordingID:  recordingID,
		StartMs:      t.startMs,
		EndMs:        t.lastMs,
		DetectionIDs: string(idsJSON),
		AvgEmbedding: float64sToBytes(t.avgEmbedding),
		FrameCount:   len(t.detectionIDs),
		Confidence:   t.totalConf / float64(len(t.detectionIDs)),
	}
}

// BuildTracks groups detections into tracks based on temporal proximity
// and embedding similarity. Detections without embeddings are skipped.
func BuildTracks(recordingID int64, detections []model.Detection, embeddings map[int64][]float64, cfg Config) []model.Track {
	// Sort detections by timestamp
	sort.Slice(detections, func(i, j int) bool {
		return detections[i].TimestampMs < detections[j].TimestampMs
	})

	var activeTracks []openTrack

	for _, det := range detections {
		emb, ok := embeddings[det.ID]
		if !ok || len(emb) == 0 {
			continue
		}

		bestIdx := -1
		bestSim := cfg.SimThreshold

		for i := range activeTracks {
			t := &activeTracks[i]
			gap := det.TimestampMs - t.lastMs

			// Track has expired
			if gap > cfg.MaxGapMs {
				continue
			}

			sim := cosineSimilarity(emb, t.avgEmbedding)
			if sim > bestSim {
				bestSim = sim
				bestIdx = i
			}
		}

		if bestIdx >= 0 {
			activeTracks[bestIdx].addDetection(det, emb)
		} else {
			activeTracks = append(activeTracks, newOpenTrack(det, emb))
		}
	}

	// Finalize all tracks
	tracks := make([]model.Track, 0, len(activeTracks))
	for _, t := range activeTracks {
		tracks = append(tracks, t.finalize(recordingID))
	}

	return tracks
}

// MergeTracks merges tracks that are close in time and have similar embeddings.
func MergeTracks(tracks []model.Track, maxGapMs int64, simThreshold float64) []model.Track {
	if len(tracks) <= 1 {
		return tracks
	}

	// Sort by start time
	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].StartMs < tracks[j].StartMs
	})

	merged := []model.Track{tracks[0]}
	for i := 1; i < len(tracks); i++ {
		last := &merged[len(merged)-1]
		curr := tracks[i]

		gap := curr.StartMs - last.EndMs
		if gap <= maxGapMs {
			lastEmb := bytesToFloat64s(last.AvgEmbedding)
			currEmb := bytesToFloat64s(curr.AvgEmbedding)
			if len(lastEmb) > 0 && len(currEmb) > 0 && cosineSimilarity(lastEmb, currEmb) > simThreshold {
				// Merge: extend the last track
				if curr.EndMs > last.EndMs {
					last.EndMs = curr.EndMs
				}
				// Combine detection IDs
				var lastIDs, currIDs []int64
				json.Unmarshal([]byte(last.DetectionIDs), &lastIDs)
				json.Unmarshal([]byte(curr.DetectionIDs), &currIDs)
				combined, _ := json.Marshal(append(lastIDs, currIDs...))
				last.DetectionIDs = string(combined)
				last.FrameCount += curr.FrameCount
				// Average the embeddings
				for j := range lastEmb {
					lastEmb[j] = (lastEmb[j] + currEmb[j]) / 2
				}
				last.AvgEmbedding = float64sToBytes(lastEmb)
				continue
			}
		}
		merged = append(merged, curr)
	}

	return merged
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func float64sToBytes(vals []float64) []byte {
	buf := make([]byte, len(vals)*8)
	for i, v := range vals {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	return buf
}

func bytesToFloat64s(data []byte) []float64 {
	n := len(data) / 8
	vals := make([]float64, n)
	for i := 0; i < n; i++ {
		vals[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
	}
	return vals
}
