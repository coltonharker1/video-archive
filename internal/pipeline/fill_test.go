package pipeline

import (
	"reflect"
	"testing"

	"github.com/colton/video-archive/internal/model"
)

func TestPlanFillWindows(t *testing.T) {
	det := func(ms int64) model.Detection { return model.Detection{TimestampMs: ms} }

	tests := []struct {
		name      string
		dets      []model.Detection
		padding   int64
		totalMs   int64
		want      [][2]int64
	}{
		{
			name:    "empty",
			dets:    nil,
			padding: 2000,
			totalMs: 60_000,
			want:    nil,
		},
		{
			name:    "single detection mid-video",
			dets:    []model.Detection{det(10_000)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{8_000, 12_000}},
		},
		{
			name:    "detection near start is clamped to 0",
			dets:    []model.Detection{det(500)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{0, 2_500}},
		},
		{
			name:    "detection near end is clamped to duration",
			dets:    []model.Detection{det(59_500)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{57_500, 60_000}},
		},
		{
			name:    "adjacent detections merge",
			dets:    []model.Detection{det(10_000), det(12_000), det(14_000)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{8_000, 16_000}},
		},
		{
			name:    "distant detections stay separate",
			dets:    []model.Detection{det(5_000), det(30_000)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{3_000, 7_000}, {28_000, 32_000}},
		},
		{
			name:    "exactly-touching windows merge",
			dets:    []model.Detection{det(10_000), det(14_000)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{8_000, 16_000}},
		},
		{
			name:    "unsorted input is sorted defensively",
			dets:    []model.Detection{det(30_000), det(5_000)},
			padding: 2000,
			totalMs: 60_000,
			want:    [][2]int64{{3_000, 7_000}, {28_000, 32_000}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planFillWindows(tc.dets, tc.padding, tc.totalMs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("planFillWindows:\n  got  = %v\n  want = %v", got, tc.want)
			}
		})
	}
}
