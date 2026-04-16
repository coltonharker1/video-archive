# video-archive

Face-aware archival pipeline for digitized home video (VHS, camcorder). Detects
and clusters faces across a personal archive, lets you name the people, and
surfaces whole **scenes** (not just raw timestamps) when you search by person
or group.

Built for the Harker family home-video collection (~18 hours of digitized VHS
across ~43 tapes, spanning 1987–2005). Runs locally — no cloud, no account,
no upload.

## What it does

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Go CLI + HTMX Review UI                       │
│  ingest · sample · scenes · detect · track · cluster · match · ui   │
└────────────┬──────────────────────────────────┬─────────────────────┘
             │ HTTP (localhost)                   │ subprocess
             ▼                                   ▼
┌────────────────────────┐           ┌────────────────────────────┐
│   Python ML Worker     │           │          FFmpeg            │
│   FastAPI + InsightFace │           │   sample, deinterlace,    │
│   RetinaFace + ArcFace │           │   scene detection (scdet)  │
└────────────────────────┘           └────────────────────────────┘
                         ▼
                 ┌──────────────┐
                 │ SQLite (WAL) │
                 └──────────────┘
```

The pipeline: **ingest → sample → scenes → detect → embed → track → cluster
→ match → segments → scene-map**. Each stage is idempotent and resumable.
Cross-video identity matching uses **max similarity over all reference
centroids** so one identity can span different ages (baby → teen → adult)
without diluted references.

## Prerequisites

- **Go 1.22+**
- **FFmpeg** (with `ffprobe`) on `$PATH`
- **Python 3.10+** for the ML worker
- A Mac or Linux box (tested on Apple Silicon)

## Setup

```bash
# Build the Go binary
make build

# Set up the Python ML worker (one-time, ~2 GB of ML dependencies)
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

The first `worker.py` run downloads the InsightFace models (~300 MB) into
`~/.video-archive/models/` (or `~/.insightface/`).

Data lives under `~/.video-archive/`:

```
~/.video-archive/
├── va.db                    # SQLite database
├── masters/YYYY/            # ingested source videos (hardlinked by default)
├── frames/{recording_id}/   # extracted frame JPEGs
├── crops/{recording_id}/    # face crops
└── logs/
```

## Usage

Start the ML worker in one terminal:

```bash
cd worker
source .venv/bin/activate
python worker.py
```

Then from the repo root:

```bash
# Single-video flow
./va run /path/to/video.mp4
./va review                   # opens http://127.0.0.1:8090

# Batch flow (full archive)
./va ingest-dir ~/Desktop/Home\ Videos --link
caffeinate -dis ./va run-all
./va review
```

### Key commands

| Command | Purpose |
|---------|---------|
| `va ingest <file>` | Single-file ingest (extracts metadata, stores master) |
| `va ingest-dir <dir>` | Recursive batch ingest, extracts dates from filenames |
| `va run <file>` | Full pipeline on one file |
| `va run-all` | Full pipeline on every unprocessed recording (smallest first) |
| `va scenes <id>` | Shot-boundary detection (tunable via `--threshold`) |
| `va scene-map <id>` | Face-informed scene merging + people attribution |
| `va segments <id>` | Per-person time segments |
| `va review [id]` | HTMX web UI for naming/merging/rejecting clusters |
| `va list`, `va show <id>`, `va status` | Inspect the archive |

Run any command with `--help` for flags.

### Review UI

- `/` — archive home page with per-recording stats
- `/review/{id}` — cluster grid with keyboard shortcuts (`j/k` navigate, `n`
  name, `r` reject, `?` help)
- `/scenes/{id}` — detected scenes with people attributed, click to play
- `/identities` and `/identities/{id}` — per-person browsing
- `/groups` — manage named groups (e.g. "Harker Family")

## Design

See [DESIGN.md](./DESIGN.md) for the full architecture, database schema,
scene-detection approach (including VHS-specific preprocessing), age
progression handling, and scaling strategy for the full archive.

## Status

Personal project, not intended for general distribution. The VHS-specific
tuning (threshold=10 with `yadif + hqdn3d` preprocessing, 500ms segment
overlap minimum, Jaccard-based scene merging) is calibrated for the Harker
archive and probably needs re-tuning on other source material.
