# Virtual TV Station 📺

A robust, self-hosted video streaming application in Go that simulates a 24/7 live broadcast. Stream any video file as if it were a live TV channel with perfect synchronization across all viewers.

## Features

- **Virtual Live Broadcasting**: All viewers see the same moment in the video, regardless of when they join
- **Resource-Efficient**: FFmpeg only runs when viewers are actively watching
- **Web Dashboard**: Beautiful dark-mode UI with real-time statistics
- **Dual Streaming Modes**: 
  - **HLS**: Standard stream on port 8093 (High compatibility)
  - **LLHLS**: Low-Latency stream on port 3333 (Real-time, <2s latency)
- **Performance**: Docker setup uses RAM disk (`tmpfs`) for zero-latency segment writing
- **CORS Support**: Built-in CORS middleware for external players
- **Tailscale Integration**: Secure remote access through Tailscale VPN
- **Auto-Recovery**: Automatic stream recovery and error handling
- **Infinite Loop**: Videos play continuously in a seamless loop

## Prerequisites

- **Go 1.20+**: For building the application
- **FFmpeg**: Must be installed and available in PATH
- **Tailscale** (optional): For secure remote access
- **Linux/macOS**: Recommended for production use (Windows supported for development)

## Installation

### Install FFmpeg

Ubuntu/Debian:
```bash
sudo apt update
sudo apt install ffmpeg
```

macOS:
```bash
brew install ffmpeg
```

Windows:
Download from [ffmpeg.org](https://ffmpeg.org/download.html) and add to PATH

### Install Tailscale (Optional)

Ubuntu/Debian:
```bash
curl -fsSL https://tailscale.com/install.sh | sh
```

macOS:
```bash
brew install tailscale
```

## Building

```bash
# Clone or download the project files
cd virtual-tv-station

# Build the application
go build -o virtual-tv-station main.go
```

## Configuration

The application is configured via Environment Variables. When running with Docker, these can be set in `docker-compose.yml` or passed via `docker run -e`.

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8093 | Port for Dashboard + HLS Stream |
| `LLHLS_PORT` | 3333 | Port for LLHLS Stream |
| `VIDEO_PATH` | `video.mp4` | Path to the source video file inside the container/system |

### Source Code Constants
For development, you can also modify the defaults in `main.go` (vars section):

```go
var (
    DefaultPort = 8093
    LLHLSPort   = 3333
    VideoPath   = "video.mp4"
    // ...
)
```

### Hardware Acceleration (Optional)

For better performance with large files, enable hardware encoding by modifying line 221 in `main.go`:

- NVIDIA: Change `"libx264"` to `"h264_nvenc"`
- Intel QuickSync: Change `"libx264"` to `"h264_qsv"` 
- AMD: Change `"libx264"` to `"h264_amf"`

## Running

### Basic Usage

```bash
# Run the compiled binary
./virtual-tv-station
```

### As a System Service (Linux)

Create `/etc/systemd/system/virtual-tv-station.service`:

```ini
[Unit]
Description=Virtual TV Station
After=network.target

[Service]
Type=simple
User=yourusername
WorkingDirectory=/path/to/virtual-tv-station
ExecStart=/path/to/virtual-tv-station/virtual-tv-station
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable virtual-tv-station
sudo systemctl start virtual-tv-station
```

### With Docker (Recommended)

1. Place your video file in the project directory (e.g., `video.mp4`).
2. Run with Docker Compose:

```bash
docker-compose up --build -d
```

The dashboard will be available at `http://localhost:8093`.

To change the video file, update the `volumes` section in `docker-compose.yml`:

```yaml
volumes:
  - /absolute/path/to/my-movie.mp4:/video.mp4
```

### With Docker CLI

```bash
docker build -t virtual-tv-station .
docker run -d \
  -p 8093:8093 \
  -p 3333:3333 \
  -v $(pwd)/video.mp4:/video.mp4 \
  -e VIDEO_PATH=/video.mp4 \
  virtual-tv-station
```

## Usage

1. **Access Dashboard**: Open `http://localhost:8093` in your browser
2. **Standard Stream (HLS)**: 
   - Playlist: `http://localhost:8093/stream.m3u8`
   - Best for: Compatibility, stability, mobile devices
3. **Low-Latency Stream (LLHLS)**:
   - Playlist: `http://localhost:3333/stream.m3u8`
   - Best for: Real-time sync, minimum latency
   - Note: Requires player support for LLHLS/fmp4

### Client Applications

The stream is compatible with any HLS player:

- **VLC**: File → Open Network Stream → `http://yourserver:8080/stream.m3u8`
- **MPV**: `mpv http://yourserver:8080/stream.m3u8`
- **Web**: Use HLS.js (included in dashboard)
- **Mobile**: Most mobile browsers support HLS natively

## How It Works

### Virtual Live Logic

1. On first run, creates `genesis.json` with the station's "birth" timestamp
2. Calculates current playback position: `(current_time - genesis_time) % video_duration`
3. Ensures segment numbering never resets: `(current_time - genesis_time) / segment_duration`
4. All viewers receive the same segments at the same time

### Resource Management

- FFmpeg starts only when the first viewer requests the stream
- Automatic shutdown after 30 seconds of no activity
- Viewer tracking with 60-second timeout
- Efficient segment cleanup to prevent disk bloat

## API Endpoints

### Port 8093 (Main/HLS)
- `GET /` - Web dashboard
- `GET /api/stats` - JSON statistics
- `GET /stream.m3u8` - HLS playlist
- `GET /segment?name={name}` - HLS TS segments

### Port 3333 (LLHLS)
- `GET /stream.m3u8` - LLHLS playlist
- `GET /segment?name={name}` - LLHLS fmp4/m4s segments

### Stats API Response

```json
{
  "status": "Online",
  "viewer_count": 3,
  "current_playing": "01:45:20",
  "tailscale_up": true,
  "cpu_usage": 0.0,
  "is_running": true
}
```

## Troubleshooting

### Stream Not Starting
- Check FFmpeg is installed: `ffmpeg -version`
- Verify video file path is correct
- Check file permissions
- Review logs for FFmpeg errors

### Tailscale Issues
- Ensure Tailscale daemon is running: `tailscale status`
- Start if needed: `sudo systemctl start tailscaled`
- To disable Tailscale check, comment out the middleware in `main.go`

### Performance Issues
- Enable hardware acceleration (see Configuration)
- Reduce video quality by adjusting CRF value (lower = better quality)
- Increase segment duration for lower latency networks
- Use SSD for output directory

### Sync Issues
- Delete `genesis.json` to reset station time
- Ensure system clock is accurate
- Check network latency

## Development

### Project Structure
```
virtual-tv-station/
├── main.go          # Main application
├── dashboard.html   # Web UI (embedded)
├── genesis.json     # Station start time (generated)
├── stream/          # HLS output directory (generated)
└── README.md        # Documentation
```

### Key Functions
- `calculateCurrentPosition()` - Determines current playback position
- `startFFmpeg()` - Spawns FFmpeg process with correct seek time
- `watchdog()` - Monitors activity and stops idle streams
- `tailscaleMiddleware()` - Validates Tailscale connectivity

## License

MIT License - Feel free to use and modify for your needs.

## Credits

Built with Go, FFmpeg, and HLS.js