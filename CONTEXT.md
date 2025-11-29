# Project Context: Virtual TV Station

## Overview
This application simulates a 24/7 live TV station using a looped video file. It ensures all viewers see the exact same moment of the video (synchronized playback) using a "Genesis Time" calculation.

## Architecture
The application is a single Go binary that orchestrates:
1.  **StreamManager**: The core logic engine.
    *   Calculates seek position based on `(Now - Genesis)`.
    *   Manages the FFmpeg process (start/stop/idle).
    *   Tracks viewers and handles cleanup.
2.  **Dual HTTP Servers**:
    *   **HLS Server (Port 8093)**: Serves the Web Dashboard, Stats API, and Standard HLS (MPEG-TS, 4s segments).
    *   **LLHLS Server (Port 3333)**: Serves Low-Latency HLS (fmp4, 1s segments).
3.  **FFmpeg Engine**:
    *   Runs as a child process.
    *   Produces **two simultaneous outputs** from the single source video.
    *   Output 1: `./stream/hls` (Standard)
    *   Output 2: `./stream/llhls` (Low Latency)
    *   **Optimization**: Writes segments to a RAM disk (`tmpfs`) at `/app/stream` for high performance.
4.  **Dockerization**:
    *   `Dockerfile`: Multi-stage build with FFmpeg integration.
    *   `docker-compose.yml`: Orchestration with volume mapping for video injection.
    *   **Configuration**: Uses environment variables (`VIDEO_PATH`, `PORT`) injected via `init()`.

## Key Files
*   `main.go`: Contains all server logic, stream management, and handler factories. Reads env vars in `init()`.
    *   **Middlewares**: `corsMiddleware` (headers) and `tailscaleMiddleware` (security).
*   `Dockerfile` & `docker-compose.yml`: Container definition and deployment config.
*   `dashboard.html`: Embedded frontend UI.

## Recent Changes
*   Split architecture to support HLS (8093) and LLHLS (3333) concurrently.
*   Updated `StreamManager` to calculate two different `startNumber` sequences (one for 4s segments, one for 1s segments).
*   Refactored HTTP handlers into factory functions to support multiple output directories.
*   Containerized the application with Docker and added Environment Variable support.
*   Implemented **RAM Disk (tmpfs)** for segment storage to reduce disk I/O.
*   Added **CORS middleware** to support external players and verified Tailscale integration.
*   Containerized the application with Docker and added Environment Variable support.

## Warm-Up Instructions
To provide full context to the agent, ask it to:
"Read the README.md and CONTEXT.md files to understand the current architecture and dual-stream setup."
