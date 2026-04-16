package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
)

// MatchOptions controls cross-video identity matching.
type MatchOptions struct {
	Threshold float64 // cosine similarity threshold for auto-matching (default 0.5)
	AutoApply bool    // automatically confirm matches above threshold (vs just suggest)
}

// DefaultMatchOptions returns sensible defaults.
func DefaultMatchOptions() MatchOptions {
	return MatchOptions{
		Threshold: 0.5,
		AutoApply: false,
	}
}

// MatchResult describes the outcome of identity matching.
type MatchResult struct {
	Matched   int // clusters matched to existing identities
	Unmatched int // clusters with no match
}

// Match holds a proposed cluster-to-identity match.
type Match struct {
	ClusterID    int64
	IdentityID   int64
	IdentityName string
	Similarity   float64
}

// MatchIdentities compares pending clusters in a recording against all known
// identities from the entire archive. For each pending cluster, finds the
// best-matching identity above the threshold.
func MatchIdentities(_ config.Config, db *store.DB, recordingID int64, opts MatchOptions) ([]Match, *MatchResult, error) {
	// Load pending clusters for this recording
	clusters, err := db.ListClusters(recordingID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing clusters: %w", err)
	}

	// Build reference embeddings from all confirmed clusters across ALL recordings
	// (not just this one). Each identity gets an average embedding from its clusters.
	identityEmbs, err := buildIdentityEmbeddings(db)
	if err != nil {
		return nil, nil, fmt.Errorf("building identity embeddings: %w", err)
	}

	if len(identityEmbs) == 0 {
		slog.Info("no known identities to match against", "recording_id", recordingID)
		return nil, &MatchResult{Unmatched: len(clusters)}, nil
	}

	var matches []Match
	matched, unmatched := 0, 0

	for _, c := range clusters {
		if c.Status != "pending" {
			continue
		}

		clusterEmb := bytesToFloat64s(c.CentroidEmb)
		if len(clusterEmb) == 0 {
			unmatched++
			continue
		}

		// Find best matching identity using max similarity over all reference
		// centroids per identity. Averaging the refs dilutes matches when an
		// identity spans multiple ages (ArcFace embeddings are not invariant
		// to age) or very different poses/lighting conditions.
		bestSim := -1.0
		bestIdentityID := int64(0)
		bestName := ""

		for identityID, ref := range identityEmbs {
			for _, v := range ref.vectors {
				sim := cosineSim(clusterEmb, v)
				if sim > bestSim {
					bestSim = sim
					bestIdentityID = identityID
					bestName = ref.name
				}
			}
		}

		if bestSim >= opts.Threshold {
			m := Match{
				ClusterID:    c.ID,
				IdentityID:   bestIdentityID,
				IdentityName: bestName,
				Similarity:   bestSim,
			}
			matches = append(matches, m)

			if opts.AutoApply {
				if err := db.UpdateClusterIdentity(c.ID, bestIdentityID); err != nil {
					slog.Warn("failed to auto-apply match", "cluster_id", c.ID, "error", err)
				}
			}
			matched++
		} else {
			unmatched++
		}
	}

	slog.Info("identity matching complete",
		"recording_id", recordingID,
		"known_identities", len(identityEmbs),
		"matched", matched,
		"unmatched", unmatched,
		"threshold", opts.Threshold,
		"auto_apply", opts.AutoApply,
	)

	return matches, &MatchResult{Matched: matched, Unmatched: unmatched}, nil
}

type identityRef struct {
	name    string
	vectors [][]float64 // every confirmed cluster centroid — matched via max similarity
}

// buildIdentityEmbeddings collects every confirmed cluster centroid per
// identity. Matching uses max similarity over this set rather than the
// average, preserving distinguishable references for age, pose, and
// lighting variation under a single identity.
func buildIdentityEmbeddings(db *store.DB) (map[int64]*identityRef, error) {
	identities, err := db.ListIdentities()
	if err != nil {
		return nil, err
	}
	if len(identities) == 0 {
		return nil, nil
	}

	recs, err := db.ListRecordings()
	if err != nil {
		return nil, err
	}

	result := make(map[int64]*identityRef, len(identities))
	for _, ident := range identities {
		result[ident.ID] = &identityRef{name: ident.Name}
	}

	for _, rec := range recs {
		clusters, err := db.ListClusters(rec.ID)
		if err != nil {
			continue
		}
		for _, c := range clusters {
			if c.Status != "confirmed" || c.IdentityID == nil {
				continue
			}
			emb := bytesToFloat64s(c.CentroidEmb)
			if len(emb) == 0 {
				continue
			}
			if ref, ok := result[*c.IdentityID]; ok {
				ref.vectors = append(ref.vectors, emb)
			}
		}
	}

	// Drop identities with no confirmed reference centroids — nothing to match against.
	for id, ref := range result {
		if len(ref.vectors) == 0 {
			delete(result, id)
		}
	}

	return result, nil
}

// unused — keep json import for track ID parsing in other files
var _ = json.Marshal
