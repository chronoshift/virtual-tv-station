package main

import (
	_ "embed"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
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
var (
	DefaultPort          = 8093
	LLHLSPort            = 3333
	VideoPath            = "video.mp4"
	OutputDirHLS         = "./stream/hls"
	OutputDirLLHLS       = "./stream/llhls"
	SegmentDurationHLS   = 4
	SegmentDurationLLHLS = 1
	IdleTimeout          = 30 * time.Second
	StatsUpdatePeriod    = 2 * time.Second
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
	isRunning      bool
	
	// Viewer tracking
	viewersHLS     map[string]time.Time
	viewersLLHLS   map[string]time.Time
	viewersMutex   sync.Mutex
}

// Stats response for the dashboard
type Stats struct {
	Status         string   `json:"status"`
	ViewerCount    int      `json:"viewer_count"`
	ViewersHLS     []string `json:"viewers_hls"`
	ViewersLLHLS   []string `json:"viewers_llhls"`
	CurrentPlaying string   `json:"current_playing"`
	IsRunning      bool     `json:"is_running"`
	CPUUsage       string   `json:"cpu_usage"` // Placeholder
}

var streamManager *StreamManager

func init() {
	// Environment overrides
	if p := os.Getenv("PORT"); p != "" {
		if i, err := strconv.Atoi(p); err == nil {
			DefaultPort = i
		}
	}
	if p := os.Getenv("LLHLS_PORT"); p != "" {
		if i, err := strconv.Atoi(p); err == nil {
			LLHLSPort = i
		}
	}
	if v := os.Getenv("VIDEO_PATH"); v != "" {
		VideoPath = v
	}
}

func main() {
	// Create output directories
	os.MkdirAll(OutputDirHLS, 0755)
	os.MkdirAll(OutputDirLLHLS, 0755)

	streamManager = &StreamManager{
		viewersHLS:   make(map[string]time.Time),
		viewersLLHLS: make(map[string]time.Time),
		lastAccess:   time.Now(),
	}

	if err := streamManager.loadGenesis(); err != nil {
		log.Fatalf("Failed to load genesis: %v", err)
	}

	if err := streamManager.getVideoDuration(); err != nil {
		log.Fatalf("Failed to get video duration: %v", err)
	}

	// Start background tasks
	go streamManager.watchdog()
	go streamManager.cleanupViewers()

	// HLS Server
	muxHLS := http.NewServeMux()
	muxHLS.HandleFunc("/", corsMiddleware(http.HandlerFunc(handleDashboard)))
	muxHLS.Handle("/hls/", http.StripPrefix("/hls", corsMiddleware(createStreamHandler(OutputDirHLS, "video/MP2T", "stream.m3u8", "hls"))))
	muxHLS.HandleFunc("/api/stats", corsMiddleware(http.HandlerFunc(handleStats)))

	serverHLS := &http.Server{
		Addr:    fmt.Sprintf(":%d", DefaultPort),
		Handler: muxHLS,
	}

	// LLHLS Server
	muxLLHLS := http.NewServeMux()
	muxLLHLS.Handle("/app/stream/", http.StripPrefix("/app/stream", corsMiddleware(createStreamHandler(OutputDirLLHLS, "video/iso.segment", "llhls.m3u8", "llhls"))))

	serverLLHLS := &http.Server{
		Addr:    fmt.Sprintf(":%d", LLHLSPort),
		Handler: muxLLHLS,
	}

	// Start servers
	go func() {
		log.Printf("Starting HLS Station on port %d", DefaultPort)
		if err := serverHLS.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HLS Server error: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting LLHLS Station on port %d", LLHLSPort)
		if err := serverLLHLS.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("LLHLS Server error: %v", err)
		}
	}()

	// Graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	log.Println("Shutting down...")
	streamManager.stopFFmpeg()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverHLS.Shutdown(ctx)
	serverLLHLS.Shutdown(ctx)
}

// Handlers

func corsMiddleware(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			return
		}
		next.ServeHTTP(w, r)
	}
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(dashboardHTML))
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	viewersHLS, viewersLLHLS := streamManager.getViewerStats()
	total := len(viewersHLS) + len(viewersLLHLS)
	
	status := "Idle"
	if streamManager.isRunning {
		status = "Broadcasting"
	}

	stats := Stats{
		Status:         status,
		ViewerCount:    total,
		ViewersHLS:     viewersHLS,
		ViewersLLHLS:   viewersLLHLS,
		CurrentPlaying: streamManager.getCurrentPlayingTime(),
		IsRunning:      streamManager.isRunning,
		CPUUsage:       "0%", // Placeholder for now
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func createStreamHandler(outputDir string, contentType string, playlistAlias string, streamType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		name := strings.TrimPrefix(path, "/")
		if name == "" {
			http.NotFound(w, r)
			return
		}

		// Handle playlist alias
		if name == playlistAlias {
			name = "stream.m3u8"
		}

		// Track viewer
		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = forwarded
		}
		// If X-Forwarded-For contains multiple IPs, take the first one
		if strings.Contains(ip, ",") {
			ip = strings.TrimSpace(strings.Split(ip, ",")[0])
		}
		
		streamManager.trackViewer(ip, streamType)

		// Start FFmpeg if not running
		if !streamManager.isRunning {
			if err := streamManager.startFFmpeg(); err != nil {
				log.Printf("Failed to start FFmpeg: %v", err)
				http.Error(w, "Failed to start stream", http.StatusInternalServerError)
				return
			}
		}

		streamManager.updateLastAccess()

		filePath := filepath.Join(outputDir, name)

		// Wait loop for file existence
		for i := 0; i < 20; i++ {
			if _, err := os.Stat(filePath); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}

		// Set Content-Type
		if strings.HasSuffix(name, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		} else if strings.HasSuffix(name, ".ts") {
			w.Header().Set("Content-Type", "video/MP2T")
		} else if strings.HasSuffix(name, ".m4s") {
			w.Header().Set("Content-Type", "video/iso.segment")
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filePath)
	}
}

// StreamManager Methods

func (sm *StreamManager) loadGenesis() error {
	data, err := os.ReadFile("genesis.json")
	if err == nil {
		var g Genesis
		if err := json.Unmarshal(data, &g); err == nil {
			sm.genesis = &g
			log.Printf("Loaded genesis time: %s", time.Unix(g.StartTime, 0))
			return nil
		}
	}

	// Create new genesis
	sm.genesis = &Genesis{StartTime: time.Now().Unix()}
	data, _ = json.Marshal(sm.genesis)
	os.WriteFile("genesis.json", data, 0644)
	log.Printf("Created new genesis time: %s", time.Unix(sm.genesis.StartTime, 0))
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

func (sm *StreamManager) calculateCurrentPosition() (seekTime float64, startNumberHLS, startNumberLLHLS int64) {
	now := time.Now().Unix()
	elapsed := float64(now - sm.genesis.StartTime)
	
	// Calculate position in loop
	seekTime = elapsed
	for seekTime >= sm.videoDuration {
		seekTime -= sm.videoDuration
	}
	
	// Calculate monotonic start numbers
	startNumberHLS = int64(elapsed / float64(SegmentDurationHLS))
	startNumberLLHLS = int64(elapsed / float64(SegmentDurationLLHLS))
	
	return
}

func (sm *StreamManager) startFFmpeg() error {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if sm.isRunning {
		return nil
	}
	
	seekTime, startNumberHLS, startNumberLLHLS := sm.calculateCurrentPosition()
	
	log.Printf("Starting FFmpeg at seek: %.2f, HLS segment: %d, LLHLS segment: %d", seekTime, startNumberHLS, startNumberLLHLS)
	
	// Clean up old segments
	files, _ := filepath.Glob(filepath.Join(OutputDirHLS, "*.ts"))
	for _, f := range files { os.Remove(f) }
	files, _ = filepath.Glob(filepath.Join(OutputDirLLHLS, "*.m4s"))
	for _, f := range files { os.Remove(f) }
	files, _ = filepath.Glob(filepath.Join(OutputDirLLHLS, "*.mp4"))
	for _, f := range files { os.Remove(f) }

	args := []string{
		"-re",
		"-ss", fmt.Sprintf("%.2f", seekTime),
		"-i", VideoPath,
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",
		
		// Output 1: HLS (Standard)
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", SegmentDurationHLS),
		"-hls_list_size", "5",
		"-hls_flags", "delete_segments",
		"-start_number", fmt.Sprintf("%d", startNumberHLS),
		"-hls_segment_filename", filepath.Join(OutputDirHLS, "segment%d.ts"),
		filepath.Join(OutputDirHLS, "stream.m3u8"),

		// Output 2: LLHLS (Low Latency - approximated)
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", SegmentDurationLLHLS),
		"-hls_list_size", "10", 
		"-hls_flags", "delete_segments",
		"-hls_segment_type", "fmp4",
		"-start_number", fmt.Sprintf("%d", startNumberLLHLS),
		"-hls_segment_filename", filepath.Join(OutputDirLLHLS, "segment%d.m4s"),
		filepath.Join(OutputDirLLHLS, "stream.m3u8"),
	}
	
	sm.ffmpegCmd = exec.Command("ffmpeg", args...)
	sm.ffmpegCmd.Stdout = os.Stdout
	sm.ffmpegCmd.Stderr = os.Stderr
	
	if err := sm.ffmpegCmd.Start(); err != nil {
	go func() { sm.ffmpegCmd.Wait() }()
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	go func() { sm.ffmpegCmd.Wait() }()
	}
	go func() { sm.ffmpegCmd.Wait() }()
	
	sm.isRunning = true
	sm.lastAccess = time.Now()
	
	// Wait for playlist
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(OutputDirHLS, "stream.m3u8")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	
	return nil
}

func (sm *StreamManager) stopFFmpeg() {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if sm.ffmpegCmd != nil && sm.ffmpegCmd.Process != nil {
		sm.ffmpegCmd.Process.Kill()
	}
	sm.isRunning = false
}

func (sm *StreamManager) updateLastAccess() {
	sm.ffmpegMutex.Lock()
	sm.lastAccess = time.Now()
	sm.ffmpegMutex.Unlock()
}

func (sm *StreamManager) watchdog() {
	for {
		time.Sleep(5 * time.Second)
		sm.ffmpegMutex.Lock()
		if sm.isRunning && time.Since(sm.lastAccess) > IdleTimeout {
			log.Println("Idle timeout reached, stopping FFmpeg")
			if sm.ffmpegCmd != nil && sm.ffmpegCmd.Process != nil {
				sm.ffmpegCmd.Process.Kill()
			}
			sm.isRunning = false
		}
		sm.ffmpegMutex.Unlock()
	}
}

// Viewer Tracking

func (sm *StreamManager) trackViewer(ip string, streamType string) {
	sm.viewersMutex.Lock()
	defer sm.viewersMutex.Unlock()
	
	now := time.Now()
	if streamType == "hls" {
		sm.viewersHLS[ip] = now
	} else if streamType == "llhls" {
		sm.viewersLLHLS[ip] = now
	}
}

func (sm *StreamManager) getViewerStats() (hlsViewers []string, llhlsViewers []string) {
	sm.viewersMutex.Lock()
	defer sm.viewersMutex.Unlock()
	
	cutoff := time.Now().Add(-60 * time.Second)
	
	for ip, lastSeen := range sm.viewersHLS {
		if lastSeen.After(cutoff) {
			hlsViewers = append(hlsViewers, ip)
		}
	}
	sort.Strings(hlsViewers)
	
	for ip, lastSeen := range sm.viewersLLHLS {
		if lastSeen.After(cutoff) {
			llhlsViewers = append(llhlsViewers, ip)
		}
	}
	sort.Strings(llhlsViewers)
	
	return
}

func (sm *StreamManager) cleanupViewers() {
	for {
		time.Sleep(10 * time.Second)
		sm.viewersMutex.Lock()
		cutoff := time.Now().Add(-60 * time.Second)
		
		for ip, lastSeen := range sm.viewersHLS {
			if lastSeen.Before(cutoff) {
				delete(sm.viewersHLS, ip)
			}
		}
		for ip, lastSeen := range sm.viewersLLHLS {
			if lastSeen.Before(cutoff) {
				delete(sm.viewersLLHLS, ip)
			}
		}
		sm.viewersMutex.Unlock()
	}
}

func (sm *StreamManager) getCurrentPlayingTime() string {
	now := time.Now().Unix()
	elapsed := float64(now - sm.genesis.StartTime)
	
	// Calculate position in loop
	seekTime := elapsed
	for seekTime >= sm.videoDuration {
		seekTime -= sm.videoDuration
	}
	
	hours := int(seekTime / 3600)
	minutes := int((seekTime - float64(hours*3600)) / 60)
	seconds := int(seekTime - float64(hours*3600) - float64(minutes*60))
	
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}
