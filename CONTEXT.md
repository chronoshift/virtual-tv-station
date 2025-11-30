# Virtual TV Station Project Context

## Overview
This project simulates a live TV station by looping a video file indefinitely. It uses FFmpeg for transcoding and Go for orchestration and serving.

## Architecture
*   **Language**: Go (Golang)
*   **Transcoder**: FFmpeg
    *   **Encoder**: NVIDIA  (GPU Accelerated)
    *   **Audio**:  (Passthrough)
    *   **Tuning**:  (Low Latency)
*   **Protocol**: HLS (HTTP Live Streaming)
    *   **Standard**: MPEG-TS segments (4s duration).
    *   **Low-Latency**: fMP4 segments (1s duration).
*   **Storage**:  (RAM Disk) at  for segment storage to prevent SSD wear.

## Key Components
1.  **StreamManager ()**:
    *   Manages the FFmpeg lifecycle (start/stop/watchdog).
    *   Calculates playback position based on .
    *   Tracks viewers by IP.
    *   Monitors CPU usage via .
    *   Handles API requests for stats and control (Pause/Seek).
2.  **Dashboard ()**:
    *   Embedded HTML5/JS frontend.
    *   Uses  for playback.
    *   Displays real-time stats, client lists, and overlay progress.
    *   Provides controls to Pause, Resume, and Seek the broadcast globally.
3.  **Infrastructure**:
    *   **Docker**: Multi-stage build ( -> ).
    *   **Docker Compose**: Handles GPU reservation and volume mapping.

## Recent Changes
*   **GPU Migration**: Switched to  base image and  for ~1% CPU usage.
*   **Interactive Controls**: Added API endpoints and UI for pausing and seeking the stream.
*   **Monitoring**: Added detailed client IP lists and real-time CPU usage tracking.
*   **Overlay**: Implemented a frontend-based progress percentage overlay (replacing failed backend burn-in attempts).
*   **Reliability**: Fixed server deadlocks and zombie processes.

## Environment
*   Runs on Linux with NVIDIA drivers installed.
*   Depends on .
