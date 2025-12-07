# Virtual TV Station

A high-performance, GPU-accelerated **Virtual TV Station** that streams a looped video as a live broadcast. Features dual HLS/LL-HLS streaming, a real-time dashboard, and zero-wear RAM disk architecture.

## 🚀 Quick Start

1.  **Clone the repository**:
    ```bash
    git clone https://github.com/chronoshift/virtual-tv-station.git
    cd virtual-tv-station
    ```

2.  **Set your video path**:
    Create a `.env` file (or export the variable):
    ```bash
    echo "HOST_VIDEO_PATH=/absolute/path/to/your/video.mp4" > .env
    ```

3.  **Start the station**:
    ```bash
    docker compose up -d
    ```

4.  **Watch**: Open [http://localhost:8093](http://localhost:8093) in your browser.

## ⚡ Features

*   **Virtual Live**: Synchronized playback for all viewers.
*   **Dual Streams**: Standard HLS (Port 8093) and Low-Latency HLS (Port 3333).
*   **GPU Acceleration**: Uses NVIDIA NVENC for ~1% CPU usage.
*   **Dashboard Controls**: Pause, Resume, and Seek the broadcast globally.
*   **Zero Disk Wear**: Transcodes to RAM (`tmpfs`) to protect your SSD.
*   **Real-Time Stats**: CPU usage, viewer list (by IP), and playback progress.

## 📋 Requirements

*   **Docker** with **NVIDIA Container Toolkit** installed.
    *   [Installation Guide](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
*   **NVIDIA GPU** (Pascal or newer).
*   **Video File**: MP4/MKV format.

## ⚙️ Configuration

Configure via environment variables in `.env` or `docker-compose.yml`:

| Variable | Default | Description |
| :--- | :--- | :--- |
| `HOST_VIDEO_PATH` | `./video.mp4` | Path to the source video on your host machine. |
| `PORT` | `8093` | Port for HLS stream and Dashboard. |
| `LLHLS_PORT` | `3333` | Port for Low-Latency HLS stream. |

## 🛠️ Troubleshooting

*   **Stream not starting?** Check logs: `docker compose logs -f`. Ensure your GPU is accessible.
*   **High CPU usage?** Ensure `nvidia-smi` works on the host and the container is using the `nvidia` runtime.
*   **Playback errors?** Some browsers don't support HLS natively; the dashboard uses `hls.js` to handle this.
*   **Driver Updates:** If the stream fails after a system update with `Failed to initialize NVML`, the application will now automatically crash to force a container restart. To prevent this outage entirely, consider holding your NVIDIA driver version:
    ```bash
    sudo apt-mark hold nvidia-driver-580
    ```

## 🛡️ Reliability & Hardening

The system includes multiple self-healing mechanisms:

*   **Stall Detection**: The application monitors the HLS playlist. If it stops updating for 30 seconds (e.g., FFmpeg hang), the app crashes to trigger a restart.
*   **Startup Validation**: If the stream fails to initialize within 10 seconds, the app exits immediately.
*   **Docker Healthcheck**: The container exposes a health endpoint checked every 30s. View status with `docker ps`.

## 🏗️ Architecture

*   **Backend**: Go (Golang) for orchestration and API.
*   **Transcoder**: FFmpeg (NVENC h264_nvenc, Audio Copy).
*   **Frontend**: Embedded HTML5/JS Dashboard.
