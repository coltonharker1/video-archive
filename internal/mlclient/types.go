package mlclient

// DetectRequest is sent to the ML worker's /detect endpoint.
type DetectRequest struct {
	FramePaths []string `json:"frame_paths"`
}

// FaceDetection is a single face found in a frame.
type FaceDetection struct {
	FramePath  string      `json:"frame_path"`
	Bbox       [4]float64  `json:"bbox"`       // [x1, y1, x2, y2] in pixels
	Confidence float64     `json:"confidence"`
	Landmarks  [][2]float64 `json:"landmarks"` // 5 points
	Embedding  []float64   `json:"embedding"`  // 512-dim, L2-normalized
	Age        *int        `json:"age"`
	Gender     *string     `json:"gender"`
}

// DetectResponse is returned from the ML worker's /detect endpoint.
type DetectResponse struct {
	Detections []FaceDetection `json:"detections"`
	ElapsedMs  float64         `json:"elapsed_ms"`
}

// EmbedRequest is sent to the ML worker's /embed endpoint.
type EmbedRequest struct {
	CropPaths []string `json:"crop_paths"`
}

// EmbeddingResult is a single embedding from the /embed endpoint.
type EmbeddingResult struct {
	CropPath string    `json:"crop_path"`
	Vector   []float64 `json:"vector"`
	Quality  float64   `json:"quality"`
}

// EmbedResponse is returned from the ML worker's /embed endpoint.
type EmbedResponse struct {
	Embeddings []EmbeddingResult `json:"embeddings"`
	ElapsedMs  float64           `json:"elapsed_ms"`
}

// ClusterRequest is sent to the ML worker's /cluster endpoint.
type ClusterRequest struct {
	Vectors        [][]float64 `json:"vectors"`
	MinClusterSize int         `json:"min_cluster_size"`
}

// ClusterResponse is returned from the ML worker's /cluster endpoint.
type ClusterResponse struct {
	Labels    []int `json:"labels"`
	NClusters int   `json:"n_clusters"`
}

// HealthResponse is returned from the ML worker's /health endpoint.
type HealthResponse struct {
	Status       string  `json:"status"`
	ModelsLoaded bool    `json:"models_loaded"`
	DetThresh    float64 `json:"det_thresh"`
}
