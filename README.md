# Virtual TV Station

A Go-based "Virtual TV Station" that streams a video file in an infinite loop, simulating a live broadcast. It supports synchronized playback for all viewers, dual streams (Standard HLS and Low-Latency HLS), and now features **GPU acceleration** and **interactive dashboard controls**.

## Features

*   **Virtual Live Broadcast**: All viewers see the same moment in the video (synchronized to a persisted "genesis" time).
*   **Dual Streams**:
    *   **Standard HLS** (Port 8093): High compatibility, reliable.
    *   **Low-Latency HLS (LL-HLS)** (Port 3333): Latency < 2 seconds.
*   **GPU Acceleration**: Uses **NVIDIA NVENC** () for highly efficient transcoding (~1% CPU usage on modern systems).
*   **Dashboard Controls**:
    *   **Pause/Resume**: Globally pause the broadcast for all viewers.
    *   **Seek**: Move the broadcast to any point in the video loop.
*   **Real-time Monitoring**:
    *   **Client List**: See active viewers by IP address for each stream.
    *   **CPU Usage**: Monitor container resource usage in real-time.
    *   **Progress Overlay**: Visual playback percentage on the dashboard streams.
*   **Optimized**:
    *   **Audio Passthrough**: Copies audio track to save CPU.
    *   **RAM Disk**: Writes segments to memory () to prevent SSD wear.
    *   **Zero Latency Tuning**: Optimized encoder settings.

## Prerequisites

*   **Docker & Docker Compose** (with **NVIDIA Container Toolkit** installed).
*   **NVIDIA GPU** (Pascal or newer recommended).
*   **Video File**: A standard MP4/MKV video file.

## Quick Start

1.  **Prepare Environment**:
    Ensure you have the NVIDIA Container Toolkit set up:
    ```bash
    sudo apt-get install -y nvidia-container-toolkit
    sudo nvidia-ctk runtime configure --runtime=docker
    sudo systemctl restart docker
    ```

2.  **Run with Docker Compose**:
    Update `docker-compose.yml` to point to your video file:
    ```yaml
    volumes:
      - /path/to/your/video.mp4:/video.mp4
    ```
    Start the station:
    ```bash
    docker-compose up --build -d
    ```

3.  **Access**:
    *   **Dashboard**: [http://localhost:8093](http://localhost:8093)
    *   **HLS Playlist**: `http://localhost:8093/hls/stream.m3u8`
    *   **LL-HLS Playlist**: `http://localhost:3333/app/stream/llhls.m3u8`

## Configuration

Configured via environment variables in `docker-compose.yml`:

| Variable | Default | Description |
| :--- | :--- | :--- |
| `PORT` | 8093 | Port for HLS and Dashboard |
| `LLHLS_PORT` | 3333 | Port for LL-HLS Stream |
| `VIDEO_PATH` | /video.mp4 | Path to source video in container |

## API Endpoints

The dashboard communicates with the backend via a simple JSON API:

### GET `/api/stats`
Returns current status, viewer counts, and playback progress.
```json
{
  "status": "Broadcasting",
  "viewer_count": 5,
  "viewers_hls": ["192.168.1.5"],
  "viewers_llhls": ["192.168.1.6"],
  "current_playing": "01:23:45",
  "is_running": true,
  "is_paused": false,
  "cpu_usage": "1.2%",
  "progress": 45.5
}
```

### POST `/api/control`
Control the broadcast state.
*   **Pause**: `?action=pause`
*   **Resume**: `?action=resume`
*   **Seek**: `?action=seek&position=123.45` (seconds)

## Troubleshooting

*   **"File not found"**: Ensure FFmpeg has started (refresh the dashboard or request the playlist).
*   **GPU Errors**: Check `nvidia-smi` on the host. Ensure  is set in docker-compose.
*   **High Latency**: Ensure your player supports LL-HLS (most modern browsers do). The dashboard player uses `hls.js` with low-latency mode enabled.

## Architecture

*   **Backend**: Go (Golang 1.20+). Handles HTTP serving, API logic, and FFmpeg process management.
*   **Transcoding**: FFmpeg with NVENC. Outputting HLS (TS) and LL-HLS (fMP4) segments to a shared RAM disk.
*   **Frontend**: HTML5/JS Dashboard embedded in the Go binary.
