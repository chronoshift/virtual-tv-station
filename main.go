package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Embed the dashboard HTML
//go:embed dashboard.html
var dashboardHTML string

// Configuration
const (
	DefaultPort       = 8080
	VideoPath         = "/path/to/video.mp4" // Change this to your video file
	OutputDir         = "./stream"
	SegmentDuration   = 4 // seconds per HLS segment
	IdleTimeout       = 30 * time.Second
	StatsUpdatePeriod = 2 * time.Second
)

// Genesis represents the station's start time
type Genesis struct {
	StartTime int64 `json:"start_time"`
}

// StreamManager handles the virtual live logic
type StreamManager struct {
	genesis        *Genesis
	videoDuration  float64
	ffmpegCmd      *exec.Cmd
	ffmpegMutex    sync.Mutex
	lastAccess     time.Time
	accessMutex    sync.Mutex
	viewers        map[string]time.Time
	viewersMutex   sync.RWMutex
	isRunning      bool
	currentSeek    float64
	startNumber    int64
}

// Stats represents the current system statistics
type Stats struct {
	Status         string  `json:"status"`
	ViewerCount    int     `json:"viewer_count"`
	CurrentPlaying string  `json:"current_playing"`
	TailscaleUp    bool    `json:"tailscale_up"`
	CPUUsage       float64 `json:"cpu_usage"`
	IsRunning      bool    `json:"is_running"`
}

var streamManager *StreamManager

func main() {
	// Create output directory
	if err := os.MkdirAll(OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Initialize stream manager
	streamManager = &StreamManager{
		viewers: make(map[string]time.Time),
	}

	// Load or create genesis
	if err := streamManager.loadGenesis(); err != nil {
		log.Fatalf("Failed to load genesis: %v", err)
	}

	// Get video duration
	if err := streamManager.getVideoDuration(); err != nil {
		log.Fatalf("Failed to get video duration: %v", err)
	}

	// Start watchdog
	go streamManager.watchdog()

	// Start viewer cleanup
	go streamManager.cleanupViewers()

	// Setup HTTP routes
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/api/stats", handleStats)
	http.HandleFunc("/stream.m3u8", tailscaleMiddleware(handlePlaylist))
	http.HandleFunc("/segment", tailscaleMiddleware(handleSegment))

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	server := &http.Server{
		Addr: fmt.Sprintf(":%d", DefaultPort),
	}

	go func() {
		log.Printf("Starting Virtual TV Station on port %d", DefaultPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-sigChan
	log.Println("Shutting down...")
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	streamManager.stopFFmpeg()
	server.Shutdown(ctx)
}

func (sm *StreamManager) loadGenesis() error {
	genesisPath := "genesis.json"
	
	// Try to load existing genesis
	data, err := os.ReadFile(genesisPath)
	if err == nil {
		genesis := &Genesis{}
		if err := json.Unmarshal(data, genesis); err == nil {
			sm.genesis = genesis
			log.Printf("Loaded genesis time: %v", time.Unix(sm.genesis.StartTime, 0))
			return nil
		}
	}

	// Create new genesis
	sm.genesis = &Genesis{
		StartTime: time.Now().Unix(),
	}
	
	data, err = json.MarshalIndent(sm.genesis, "", "  ")
	if err != nil {
		return err
	}
	
	if err := os.WriteFile(genesisPath, data, 0644); err != nil {
		return err
	}
	
	log.Printf("Created new genesis time: %v", time.Unix(sm.genesis.StartTime, 0))
	return nil
}

func (sm *StreamManager) getVideoDuration() error {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		VideoPath)
	
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get video duration: %v", err)
	}
	
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return fmt.Errorf("failed to parse duration: %v", err)
	}
	
	sm.videoDuration = duration
	log.Printf("Video duration: %.2f seconds", sm.videoDuration)
	return nil
}

func (sm *StreamManager) calculateCurrentPosition() (seekTime float64, startNumber int64) {
	now := time.Now().Unix()
	elapsed := now - sm.genesis.StartTime
	
	// Calculate seek position in current loop
	seekTime = float64(elapsed) 
	for seekTime >= sm.videoDuration {
		seekTime -= sm.videoDuration
	}
	
	// Calculate segment start number (never resets)
	startNumber = elapsed / int64(SegmentDuration)
	
	return seekTime, startNumber
}

func (sm *StreamManager) startFFmpeg() error {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if sm.isRunning {
		return nil
	}
	
	seekTime, startNumber := sm.calculateCurrentPosition()
	sm.currentSeek = seekTime
	sm.startNumber = startNumber
	
	log.Printf("Starting FFmpeg at seek: %.2f, segment: %d", seekTime, startNumber)
	
	// Clean up old segments
	files, _ := filepath.Glob(filepath.Join(OutputDir, "*.ts"))
	for _, f := range files {
		os.Remove(f)
	}
	
	// Build FFmpeg command
	args := []string{
		"-ss", fmt.Sprintf("%.2f", seekTime),
		"-i", VideoPath,
		"-c:v", "libx264", // Use hardware acceleration if available: "h264_nvenc", "h264_qsv", etc.
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", SegmentDuration),
		"-hls_list_size", "5",
		"-hls_flags", "delete_segments",
		"-start_number", fmt.Sprintf("%d", startNumber),
		"-hls_segment_filename", filepath.Join(OutputDir, "segment%d.ts"),
		"-stream_loop", "-1",
		filepath.Join(OutputDir, "stream.m3u8"),
	}
	
	sm.ffmpegCmd = exec.Command("ffmpeg", args...)
	sm.ffmpegCmd.Stdout = os.Stdout
	sm.ffmpegCmd.Stderr = os.Stderr
	
	if err := sm.ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	}
	
	sm.isRunning = true
	sm.updateLastAccess()
	
	// Wait for playlist to be generated
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(OutputDir, "stream.m3u8")); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	
	return nil
}

func (sm *StreamManager) stopFFmpeg() {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if !sm.isRunning || sm.ffmpegCmd == nil {
		return
	}
	
	log.Println("Stopping FFmpeg...")
	
	if sm.ffmpegCmd.Process != nil {
		sm.ffmpegCmd.Process.Signal(syscall.SIGTERM)
		sm.ffmpegCmd.Wait()
	}
	
	sm.isRunning = false
	sm.ffmpegCmd = nil
}

func (sm *StreamManager) updateLastAccess() {
	sm.accessMutex.Lock()
	defer sm.accessMutex.Unlock()
	sm.lastAccess = time.Now()
}

func (sm *StreamManager) getLastAccess() time.Time {
	sm.accessMutex.Lock()
	defer sm.accessMutex.Unlock()
	return sm.lastAccess
}

func (sm *StreamManager) watchdog() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		if sm.isRunning {
			lastAccess := sm.getLastAccess()
			if time.Since(lastAccess) > IdleTimeout {
				log.Println("Idle timeout reached, stopping FFmpeg")
				sm.stopFFmpeg()
			}
		}
	}
}

func (sm *StreamManager) trackViewer(ip string) {
	sm.viewersMutex.Lock()
	defer sm.viewersMutex.Unlock()
	sm.viewers[ip] = time.Now()
}

func (sm *StreamManager) getViewerCount() int {
	sm.viewersMutex.RLock()
	defer sm.viewersMutex.RUnlock()
	
	count := 0
	now := time.Now()
	for _, lastSeen := range sm.viewers {
		if now.Sub(lastSeen) < 60*time.Second {
			count++
		}
	}
	return count
}

func (sm *StreamManager) cleanupViewers() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		sm.viewersMutex.Lock()
		now := time.Now()
		for ip, lastSeen := range sm.viewers {
			if now.Sub(lastSeen) > 60*time.Second {
				delete(sm.viewers, ip)
			}
		}
		sm.viewersMutex.Unlock()
	}
}

func (sm *StreamManager) getCurrentPlayingTime() string {
	seekTime, _ := sm.calculateCurrentPosition()
	hours := int(seekTime) / 3600
	minutes := (int(seekTime) % 3600) / 60
	seconds := int(seekTime) % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func checkTailscale() bool {
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		// Try to start Tailscale
		exec.Command("systemctl", "start", "tailscaled").Run()
		time.Sleep(2 * time.Second)
		
		// Check again
		output, err = cmd.Output()
		if err != nil {
			return false
		}
	}
	
	// Simple check if the output contains valid JSON
	var result map[string]interface{}
	return json.Unmarshal(output, &result) == nil
}

func tailscaleMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkTailscale() {
			http.Error(w, "Tailscale is not running", http.StatusServiceUnavailable)
			return
		}
		handler(w, r)
	}
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, nil)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	stats := Stats{
		Status:         "Idle",
		ViewerCount:    streamManager.getViewerCount(),
		CurrentPlaying: streamManager.getCurrentPlayingTime(),
		TailscaleUp:    checkTailscale(),
		IsRunning:      streamManager.isRunning,
	}
	
	if streamManager.isRunning {
		stats.Status = "Online"
	}
	
	// Get CPU usage if FFmpeg is running (simplified)
	if streamManager.isRunning && streamManager.ffmpegCmd != nil && streamManager.ffmpegCmd.Process != nil {
		// This is a placeholder - actual CPU monitoring would require more complex implementation
		stats.CPUUsage = 0.0
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func handlePlaylist(w http.ResponseWriter, r *http.Request) {
	// Track viewer
	ip := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ip = forwarded
	}
	streamManager.trackViewer(ip)
	
	// Start FFmpeg if not running
	if !streamManager.isRunning {
		if err := streamManager.startFFmpeg(); err != nil {
			http.Error(w, "Failed to start stream", http.StatusInternalServerError)
			return
		}
	}
	
	streamManager.updateLastAccess()
	
	// Serve the playlist
	playlistPath := filepath.Join(OutputDir, "stream.m3u8")
	
	// Wait for playlist to exist
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(playlistPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, playlistPath)
}

func handleSegment(w http.ResponseWriter, r *http.Request) {
	// Track viewer
	ip := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ip = forwarded
	}
	streamManager.trackViewer(ip)
	streamManager.updateLastAccess()
	
	// Get segment name from query
	segmentName := r.URL.Query().Get("name")
	if segmentName == "" {
		http.Error(w, "Missing segment name", http.StatusBadRequest)
		return
	}
	
	segmentPath := filepath.Join(OutputDir, segmentName)
	
	// Wait for segment to exist (max 5 seconds)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(segmentPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	w.Header().Set("Content-Type", "video/MP2T")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, segmentPath)
}