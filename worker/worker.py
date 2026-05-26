"""
Video Archive ML Worker — Face detection, embedding, and clustering.

Wraps insightface (SCRFD detection + ArcFace embedding) behind a FastAPI
HTTP API so the Go orchestrator can call it over localhost.

Usage:
    cd worker
    python -m venv .venv && source .venv/bin/activate
    pip install -r requirements.txt
    python worker.py                     # starts on port 8089
    python worker.py --port 9000         # custom port
    python worker.py --det-thresh 0.3    # lower detection threshold for VHS
    python worker.py --provider cpu      # force CPU (default: auto — CoreML on macOS)
"""

import argparse
import logging
import os
import platform
import sys
import time

import cv2
import numpy as np
from fastapi import FastAPI
from pydantic import BaseModel

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("worker")

# ---------------------------------------------------------------------------
# Pydantic models
# ---------------------------------------------------------------------------

class DetectRequest(BaseModel):
    frame_paths: list[str]

class FaceDetection(BaseModel):
    frame_path: str
    bbox: list[float]              # [x1, y1, x2, y2] in pixels
    confidence: float
    landmarks: list[list[float]]   # 5 points, each [x, y]
    embedding: list[float]         # 512-dim, L2-normalized
    age: int | None = None
    gender: str | None = None

class DetectResponse(BaseModel):
    detections: list[FaceDetection]
    elapsed_ms: float

class EmbedRequest(BaseModel):
    crop_paths: list[str]

class EmbeddingResult(BaseModel):
    crop_path: str
    vector: list[float]   # 512-dim
    quality: float        # blur score (higher = sharper)

class EmbedResponse(BaseModel):
    embeddings: list[EmbeddingResult]
    elapsed_ms: float

class ClusterRequest(BaseModel):
    vectors: list[list[float]]
    min_cluster_size: int = 2

class ClusterResponse(BaseModel):
    labels: list[int]   # -1 = outlier
    n_clusters: int

class HealthResponse(BaseModel):
    status: str
    models_loaded: bool
    det_thresh: float
    providers: list[str]

# ---------------------------------------------------------------------------
# App setup
# ---------------------------------------------------------------------------

app = FastAPI(title="Video Archive ML Worker")

# Global state — set during startup
face_app = None
det_thresh = 0.5
active_providers: list[str] = []


def resolve_providers(choice: str) -> list[str]:
    """Map --provider choice to an ONNX Runtime provider list.

    auto: CoreML+CPU on macOS (Apple Silicon sees the biggest win via ANE/GPU),
          CPU everywhere else. CPU is always appended so ORT falls through
          per-node if any op isn't supported by CoreML.
    """
    choice = choice.lower()
    if choice == "cpu":
        return ["CPUExecutionProvider"]
    if choice == "coreml":
        return ["CoreMLExecutionProvider", "CPUExecutionProvider"]
    if platform.system() == "Darwin":
        return ["CoreMLExecutionProvider", "CPUExecutionProvider"]
    return ["CPUExecutionProvider"]


def load_models(model_name: str = "buffalo_l", threshold: float = 0.5, provider: str = "auto"):
    """Load insightface models. Downloads automatically on first run."""
    global face_app, det_thresh, active_providers
    det_thresh = threshold

    from insightface.app import FaceAnalysis

    providers = resolve_providers(provider)
    log.info("loading insightface model pack: %s (threshold=%.2f, providers=%s)",
             model_name, threshold, providers)
    t0 = time.time()

    try:
        face_app = FaceAnalysis(name=model_name, providers=providers)
        face_app.prepare(ctx_id=0, det_thresh=threshold, det_size=(640, 640))
    except Exception as e:
        if "CoreMLExecutionProvider" in providers and providers != ["CPUExecutionProvider"]:
            log.warning("CoreML provider init failed (%s); falling back to CPU", e)
            face_app = FaceAnalysis(name=model_name, providers=["CPUExecutionProvider"])
            face_app.prepare(ctx_id=0, det_thresh=threshold, det_size=(640, 640))
        else:
            raise

    seen: set[str] = set()
    active_providers = []
    for key, model in face_app.models.items():
        sess = getattr(model, "session", None)
        sess_providers = sess.get_providers() if sess is not None else []
        log.info("  model=%s providers=%s", key, sess_providers)
        for p in sess_providers:
            if p not in seen:
                seen.add(p)
                active_providers.append(p)

    elapsed = time.time() - t0
    log.info("models loaded in %.1fs (active=%s)", elapsed, active_providers)


# ---------------------------------------------------------------------------
# Endpoints
# ---------------------------------------------------------------------------

@app.get("/health", response_model=HealthResponse)
def health():
    return HealthResponse(
        status="ok" if face_app is not None else "not_ready",
        models_loaded=face_app is not None,
        det_thresh=det_thresh,
        providers=active_providers,
    )


@app.post("/detect", response_model=DetectResponse)
def detect(req: DetectRequest):
    """
    Detect faces and generate embeddings for a batch of frame images.

    insightface's app.get() does detection + alignment + embedding in one pass,
    so we return everything at once rather than requiring separate /embed calls.
    The Go side can still call /embed separately for re-embedding crops.
    """
    if face_app is None:
        return DetectResponse(detections=[], elapsed_ms=0)

    t0 = time.time()
    detections = []

    for path in req.frame_paths:
        if not os.path.isfile(path):
            log.warning("frame not found: %s", path)
            continue

        img = cv2.imread(path)
        if img is None:
            log.warning("failed to read image: %s", path)
            continue

        faces = face_app.get(img)
        for face in faces:
            bbox = face.bbox.tolist()
            landmarks = face.landmark_2d_106 if hasattr(face, "landmark_2d_106") and face.landmark_2d_106 is not None else None
            if landmarks is None:
                # Fall back to kps (5-point keypoints)
                landmarks = face.kps.tolist() if hasattr(face, "kps") and face.kps is not None else []
            else:
                landmarks = landmarks.tolist()

            # Use the 5-point kps for the API (consistent format)
            kps = face.kps.tolist() if hasattr(face, "kps") and face.kps is not None else []

            embedding = face.embedding.tolist() if face.embedding is not None else []

            # Normalize embedding to unit vector
            if len(embedding) > 0:
                norm = np.linalg.norm(embedding)
                if norm > 0:
                    embedding = (np.array(embedding) / norm).tolist()

            detections.append(FaceDetection(
                frame_path=path,
                bbox=bbox,
                confidence=float(face.det_score),
                landmarks=kps,
                embedding=embedding,
                age=int(face.age) if hasattr(face, "age") and face.age is not None else None,
                gender=str(face.gender) if hasattr(face, "gender") and face.gender is not None else None,
            ))

    elapsed_ms = (time.time() - t0) * 1000
    log.info("detected %d faces in %d frames (%.0fms)", len(detections), len(req.frame_paths), elapsed_ms)
    return DetectResponse(detections=detections, elapsed_ms=elapsed_ms)


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    """
    Generate embeddings for pre-cropped face images.

    This is for re-embedding crops (e.g. after manual review or quality filtering).
    For initial detection, /detect already returns embeddings.
    """
    if face_app is None:
        return EmbedResponse(embeddings=[], elapsed_ms=0)

    t0 = time.time()
    results = []

    for path in req.crop_paths:
        if not os.path.isfile(path):
            log.warning("crop not found: %s", path)
            continue

        img = cv2.imread(path)
        if img is None:
            log.warning("failed to read crop: %s", path)
            continue

        # Run face detection on the crop — should find exactly one face
        faces = face_app.get(img)
        if len(faces) == 0:
            log.warning("no face found in crop: %s", path)
            continue

        face = faces[0]
        embedding = face.embedding.tolist() if face.embedding is not None else []

        # Normalize
        if len(embedding) > 0:
            norm = np.linalg.norm(embedding)
            if norm > 0:
                embedding = (np.array(embedding) / norm).tolist()

        # Compute blur score (Laplacian variance — higher = sharper)
        gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
        quality = float(cv2.Laplacian(gray, cv2.CV_64F).var())

        results.append(EmbeddingResult(
            crop_path=path,
            vector=embedding,
            quality=quality,
        ))

    elapsed_ms = (time.time() - t0) * 1000
    log.info("embedded %d crops (%.0fms)", len(results), elapsed_ms)
    return EmbedResponse(embeddings=results, elapsed_ms=elapsed_ms)


@app.post("/cluster", response_model=ClusterResponse)
def cluster(req: ClusterRequest):
    """Cluster embedding vectors using HDBSCAN."""
    if len(req.vectors) < 2:
        return ClusterResponse(labels=[-1] * len(req.vectors), n_clusters=0)

    from sklearn.cluster import HDBSCAN
    from sklearn.preprocessing import normalize

    vectors = np.array(req.vectors)
    vectors = normalize(vectors)  # ensure unit norm

    clusterer = HDBSCAN(
        min_cluster_size=req.min_cluster_size,
        min_samples=1,
        metric="euclidean",
    )
    labels = clusterer.fit_predict(vectors)

    n_clusters = len(set(labels)) - (1 if -1 in labels else 0)
    log.info("clustered %d vectors into %d clusters (%d outliers)",
             len(vectors), n_clusters, (labels == -1).sum())

    return ClusterResponse(labels=labels.tolist(), n_clusters=n_clusters)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Video Archive ML Worker")
    parser.add_argument("--port", type=int, default=8089)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--model", default="buffalo_l", help="insightface model pack name")
    parser.add_argument("--det-thresh", type=float, default=0.5,
                        help="face detection confidence threshold (lower for VHS: 0.3-0.5)")
    parser.add_argument("--provider", default="auto", choices=["auto", "coreml", "cpu"],
                        help="ONNX Runtime execution provider (auto: CoreML on macOS, CPU elsewhere)")
    args = parser.parse_args()

    load_models(model_name=args.model, threshold=args.det_thresh, provider=args.provider)

    import uvicorn
    uvicorn.run(app, host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
