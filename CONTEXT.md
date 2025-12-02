# Project Context: Virtual TV Station

## 🎯 Project Goal
To create a high-performance, self-hosted "Virtual TV Station" that streams a single video file in an infinite loop to multiple clients, synchronized globally (like a broadcast), with zero wear on physical storage.

## 🛠️ Tech Stack
*   **Core**: Go (Golang 1.20+) - Orchestration, HTTP Server, API.
*   **Transcoding**: FFmpeg with NVIDIA NVENC (`h264_nvenc`).
*   **Containerization**: Docker (Base: `nvidia/cuda:12.2.0-runtime-ubuntu22.04`).
*   **Frontend**: HTML5/JS (Embedded) using `hls.js`.
*   **Protocol**: HLS (MPEG-TS) and LL-HLS (fMP4).

## 🏗️ Architecture
The application runs as a single binary inside a GPU-enabled Docker container.

1.  **Stream Manager**:
    *   Calculates "virtual live" position based on `genesis.json` (persisted start time).
    *   Spawns `ffmpeg` process on-demand when viewers connect.
    *   Kills `ffmpeg` when idle (>30s) or paused.
    *   Calculates monotonic segment numbers so players see a continuous stream even as the file loops.

2.  **Transcoding Pipeline**:
    *   **Input**: Source video (mounted at `/video.mp4`).
    *   **Filters**: None (Burn-in overlay removed for performance).
    *   **Encoding**: GPU-accelerated (`h264_nvenc`) with `-tune ll` (Low Latency).
    *   **Audio**: Passthrough (`-c:a copy`) to minimize CPU load.
    *   **Output**: Writes segments to `/app/stream` which is a **RAM Disk** (`tmpfs`).

3.  **Serving**:
    *   **Port 8093**: Standard HLS (`/hls/stream.m3u8`), Dashboard (`/`), API (`/api/...`).
    *   **Port 3333**: Low-Latency HLS (`/app/stream/llhls.m3u8`).

## 📂 Key Files
*   `main.go`: The brain. Handles HTTP routing, FFmpeg lifecycle (`StartFFmpeg`, `StopFFmpeg`, `Watchdog`), and API logic.
*   `dashboard.html`: Embedded frontend. Features dual video players, client IP lists, CPU stats, and broadcast controls (Pause/Seek).
*   `docker-compose.yml`: Defines the service, GPU reservations, and `tmpfs` mounts. **Requires `.env` file**.
*   `Dockerfile`: Multi-stage build setup for Go + CUDA/FFmpeg environment.

## 🧠 Core Logic Notes
*   **Genesis Time**: Stored in `genesis.json`. Determines the "start" of the infinite loop.
*   **Pausing**: When paused via API, `isPaused` state is saved. On resume, `Genesis` is shifted forward so that `CurrentTime` matches the pause point.
*   **Performance**:
    *   CPU Usage: ~1.5% (due to Audio Copy + NVENC).
    *   Disk I/O: Near zero (RAM disk for segments).
    *   Viewer Tracking: Uses `RemoteAddr` or `X-Forwarded-For` to track active IPs in memory.

## 🚀 Developer Instructions
*   **Build**: `docker compose up --build -d`
*   **Env**: Create `.env` with `HOST_VIDEO_PATH=/path/to/video.mp4`.
*   **Logs**: `docker logs -f virtual-tv-station-tv-station-1`
*   **GPU Check**: `nvidia-smi` inside container must work.
