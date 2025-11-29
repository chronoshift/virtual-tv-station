# Build stage
FROM golang:1.20-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o virtual-tv-station main.go

# Runtime stage
FROM alpine:latest

# Install FFmpeg
RUN apk add --no-cache ffmpeg

WORKDIR /app

# Copy binary and web assets
COPY --from=builder /app/virtual-tv-station .
# dashboard.html is embedded in the binary, so we don't need to copy it separately
# but if there were other assets, we would copy them here

# Create output directories
RUN mkdir -p stream/hls stream/llhls

# Expose ports
EXPOSE 8093 3333

# Set environment variables
ENV PORT=8093
ENV LLHLS_PORT=3333
ENV VIDEO_PATH=/video.mp4

# Run the application
CMD ["./virtual-tv-station"]
