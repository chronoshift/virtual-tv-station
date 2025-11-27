# Virtual TV Station 📺

A robust, self-hosted video streaming application in Go that simulates a 24/7 live broadcast. Stream any video file as if it were a live TV channel with perfect synchronization across all viewers.

## Features

- **Virtual Live Broadcasting**: All viewers see the same moment in the video, regardless of when they join
- **Resource-Efficient**: FFmpeg only runs when viewers are actively watching
- **Web Dashboard**: Beautiful dark-mode UI with real-time statistics
- **HLS Streaming**: Industry-standard HTTP Live Streaming protocol
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

Edit `main.go` before building to set your video file path:

```go
const (
    DefaultPort = 8080
    VideoPath = "/path/to/your/video.mp4"  // Change this!
    OutputDir = "./stream"
    SegmentDuration = 4
    IdleTimeout = 30 * time.Second
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

### With Docker (Optional)

Create `Dockerfile`:

```dockerfile
FROM golang:1.20-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o virtual-tv-station main.go

FROM alpine:latest
RUN apk add --no-cache ffmpeg
WORKDIR /app
COPY --from=builder /app/virtual-tv-station .
COPY dashboard.html .
EXPOSE 8080
CMD ["./virtual-tv-station"]
```

Build and run:
```bash
docker build -t virtual-tv-station .
docker run -d -p 8080:8080 -v /path/to/video.mp4:/video.mp4 virtual-tv-station
```

## Usage

1. **Access Dashboard**: Open `http://localhost:8080` in your browser
2. **View Stream**: The embedded player will automatically connect
3. **Direct Stream URL**: Access HLS playlist at `http://localhost:8080/stream.m3u8`

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

- `GET /` - Web dashboard
- `GET /api/stats` - JSON statistics
- `GET /stream.m3u8` - HLS playlist (Tailscale protected)
- `GET /segment?name={name}` - Video segments (Tailscale protected)

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