package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// ClusterOptions controls clustering behavior.
type ClusterOptions struct {
	MinClusterSize int     // HDBSCAN min_cluster_size (default 2)
	MergeThreshold float64 // cosine similarity above which clusters auto-merge (default 0.55)
}

// DefaultClusterOptions returns sensible defaults for VHS footage.
func DefaultClusterOptions() ClusterOptions {
	return ClusterOptions{
		MinClusterSize: 2,
		MergeThreshold: 0.55,
	}
}

// ClusterResult describes the outcome of clustering.
type ClusterResult struct {
	ClusterCount     int
	BeforeMergeCount int
	MergedCount      int
	Skipped          bool
}

// protoCluster is a cluster being built before writing to DB.
type protoCluster struct {
	trackIndices []int
	centroid     []float64
}

// ClusterTracks groups tracks into identity clusters using HDBSCAN via the ML worker,
// then runs an auto-merge pass that combines clusters with similar centroid embeddings.
func ClusterTracks(ctx context.Context, _ config.Config, db *store.DB, ml *mlclient.Client, recordingID int64, opts ClusterOptions) (*ClusterResult, error) {
	// Check if clusters already exist
	existing, err := db.CountClusters(recordingID)
	if err != nil {
		return nil, fmt.Errorf("counting clusters: %w", err)
	}
	if existing > 0 {
		slog.Info("clusters already exist, skipping", "recording_id", recordingID, "count", existing)
		return &ClusterResult{ClusterCount: existing, Skipped: true}, nil
	}

	// Load tracks
	tracks, err := db.ListTracks(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing tracks: %w", err)
	}
	if len(tracks) == 0 {
		slog.Info("no tracks to cluster", "recording_id", recordingID)
		return &ClusterResult{}, nil
	}

	// If only 1 track, create a single cluster
	if len(tracks) == 1 {
		trackIDs, _ := json.Marshal([]int64{tracks[0].ID})
		_, err := db.InsertCluster(&model.Cluster{
			RecordingID:   &recordingID,
			TrackIDs:      string(trackIDs),
			CentroidEmb:   tracks[0].AvgEmbedding,
			ThumbnailPath: findBestThumbnail(db, tracks[0]),
			Status:        "pending",
		})
		if err != nil {
			return nil, fmt.Errorf("inserting single cluster: %w", err)
		}
		return &ClusterResult{ClusterCount: 1}, nil
	}

	// Build embedding vectors for clustering
	vectors := make([][]float64, len(tracks))
	for i, t := range tracks {
		vectors[i] = bytesToFloat64s(t.AvgEmbedding)
	}

	// Phase 1: HDBSCAN via ML worker
	result, err := ml.Cluster(ctx, vectors, opts.MinClusterSize)
	if err != nil {
		return nil, fmt.Errorf("clustering: %w", err)
	}

	// Build initial proto-clusters (including outliers as single-track clusters)
	var protoClusters []*protoCluster
	hdbscanGroups := make(map[int][]int) // label -> track indices

	for i, label := range result.Labels {
		if label == -1 {
			// Each outlier becomes its own proto-cluster
			protoClusters = append(protoClusters, &protoCluster{
				trackIndices: []int{i},
				centroid:     vectors[i],
			})
		} else {
			hdbscanGroups[label] = append(hdbscanGroups[label], i)
		}
	}

	// Create proto-clusters for HDBSCAN groups with computed centroids
	for _, indices := range hdbscanGroups {
		dim := len(vectors[0])
		centroid := make([]float64, dim)
		for _, idx := range indices {
			for k, v := range vectors[idx] {
				centroid[k] += v
			}
		}
		n := float64(len(indices))
		for k := range centroid {
			centroid[k] /= n
		}
		protoClusters = append(protoClusters, &protoCluster{
			trackIndices: indices,
			centroid:     centroid,
		})
	}

	beforeMerge := len(protoClusters)
	slog.Info("HDBSCAN phase complete",
		"recording_id", recordingID,
		"hdbscan_clusters", result.NClusters,
		"outliers", beforeMerge-result.NClusters,
		"total_proto_clusters", beforeMerge,
	)

	// Phase 2: Auto-merge clusters with similar centroids
	protoClusters = autoMergeClusters(protoClusters, opts.MergeThreshold)

	afterMerge := len(protoClusters)
	slog.Info("auto-merge phase complete",
		"recording_id", recordingID,
		"before", beforeMerge,
		"after", afterMerge,
		"merged", beforeMerge-afterMerge,
		"threshold", opts.MergeThreshold,
	)

	// Phase 3: Write final clusters to DB
	for _, pc := range protoClusters {
		trackIDs := make([]int64, len(pc.trackIndices))
		for j, idx := range pc.trackIndices {
			trackIDs[j] = tracks[idx].ID
		}
		trackIDsJSON, _ := json.Marshal(trackIDs)

		// Pick best thumbnail from highest-confidence track
		bestTrack := tracks[pc.trackIndices[0]]
		for _, idx := range pc.trackIndices[1:] {
			if tracks[idx].Confidence > bestTrack.Confidence {
				bestTrack = tracks[idx]
			}
		}

		_, err := db.InsertCluster(&model.Cluster{
			RecordingID:   &recordingID,
			TrackIDs:      string(trackIDsJSON),
			CentroidEmb:   float64sToBytes(pc.centroid),
			ThumbnailPath: findBestThumbnail(db, bestTrack),
			Status:        "pending",
		})
		if err != nil {
			return nil, fmt.Errorf("inserting cluster: %w", err)
		}
	}

	slog.Info("clustering complete",
		"recording_id", recordingID,
		"tracks", len(tracks),
		"final_clusters", afterMerge,
		"merge_threshold", opts.MergeThreshold,
	)

	return &ClusterResult{
		ClusterCount:     afterMerge,
		BeforeMergeCount: beforeMerge,
		MergedCount:      beforeMerge - afterMerge,
	}, nil
}

// autoMergeClusters iteratively merges clusters whose centroids have cosine
// similarity above the threshold. Greedy agglomerative: find most similar pair,
// merge, recompute centroid, repeat until no pair exceeds threshold.
func autoMergeClusters(clusters []*protoCluster, threshold float64) []*protoCluster {
	for {
		bestSim := -1.0
		bestI, bestJ := -1, -1

		for i := 0; i < len(clusters); i++ {
			for j := i + 1; j < len(clusters); j++ {
				sim := cosineSim(clusters[i].centroid, clusters[j].centroid)
				if sim > bestSim {
					bestSim = sim
					bestI = i
					bestJ = j
				}
			}
		}

		if bestSim < threshold || bestI < 0 {
			break
		}

		// Merge j into i with weighted average centroid
		a := clusters[bestI]
		b := clusters[bestJ]

		nA := float64(len(a.trackIndices))
		nB := float64(len(b.trackIndices))
		total := nA + nB
		newCentroid := make([]float64, len(a.centroid))
		for k := range newCentroid {
			newCentroid[k] = (a.centroid[k]*nA + b.centroid[k]*nB) / total
		}

		a.trackIndices = append(a.trackIndices, b.trackIndices...)
		a.centroid = newCentroid

		// Remove j from list
		clusters = append(clusters[:bestJ], clusters[bestJ+1:]...)
	}

	return clusters
}

func cosineSim(a, b []float64) float64 {
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

// findBestThumbnail returns the crop path of the highest-confidence detection in a track.
func findBestThumbnail(db *store.DB, track model.Track) string {
	var detIDs []int64
	json.Unmarshal([]byte(track.DetectionIDs), &detIDs)
	if len(detIDs) == 0 {
		return ""
	}

	dets, err := db.ListDetections(track.RecordingID)
	if err != nil {
		return ""
	}

	bestConf := -1.0
	bestCrop := ""
	detSet := make(map[int64]bool, len(detIDs))
	for _, id := range detIDs {
		detSet[id] = true
	}
	for _, d := range dets {
		if detSet[d.ID] && d.Confidence > bestConf {
			bestConf = d.Confidence
			bestCrop = d.CropPath
		}
	}
	return bestCrop
}
