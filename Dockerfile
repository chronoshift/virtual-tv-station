# Build stage
FROM golang:1.20-alpine AS builder

WORKDIR /app
COPY . .
RUN go build -o virtual-tv-station main.go

# Runtime stage with NVIDIA/CUDA support
FROM nvidia/cuda:12.2.0-runtime-ubuntu22.04

# Install ffmpeg and fonts
RUN apt-get update &&     apt-get install -y ffmpeg &&     rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/virtual-tv-station .

# Create output directories
RUN mkdir -p stream/hls stream/llhls

# Default environment variables
ENV PORT=8093
ENV LLHLS_PORT=3333
ENV VIDEO_PATH=/video.mp4

CMD ["./virtual-tv-station"]
