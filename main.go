package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
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
	accessMutex    sync.Mutex
	viewers        map[string]time.Time
	viewersMutex   sync.RWMutex
	isRunning        bool
	currentSeek      float64
	startNumberHLS   int64
	startNumberLLHLS int64
}

// Stats represents the current system statistics
type Stats struct {
	Status         string  `json:"status"`
	ViewerCount    int     `json:"viewer_count"`
	CurrentPlaying string  `json:"current_playing"`
	CPUUsage       float64 `json:"cpu_usage"`
	IsRunning      bool    `json:"is_running"`
}

var streamManager *StreamManager

func init() {
	if port := os.Getenv("PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			DefaultPort = p
		}
	}
	if port := os.Getenv("LLHLS_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			LLHLSPort = p
		}
	}
	if path := os.Getenv("VIDEO_PATH"); path != "" {
		VideoPath = path
	}
}

func main() {
	// Create output directories
	if err := os.MkdirAll(OutputDirHLS, 0755); err != nil {
		log.Fatalf("Failed to create HLS output directory: %v", err)
	}
	if err := os.MkdirAll(OutputDirLLHLS, 0755); err != nil {
		log.Fatalf("Failed to create LLHLS output directory: %v", err)
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

	// Setup HLS Server (Port 8093)
	muxHLS := http.NewServeMux()
	muxHLS.HandleFunc("/", corsMiddleware(handleDashboard))
	muxHLS.Handle("/hls/", http.StripPrefix("/hls", corsMiddleware(createStreamHandler(OutputDirHLS, "video/MP2T", "stream.m3u8"))))
	muxHLS.HandleFunc("/api/stats", corsMiddleware(handleStats))

	// Setup LLHLS Server (Port 3333)
	muxLLHLS := http.NewServeMux()
	muxLLHLS.Handle("/app/stream/", http.StripPrefix("/app/stream", corsMiddleware(createStreamHandler(OutputDirLLHLS, "video/iso.segment", "llhls.m3u8"))))
	// LLHLS uses fmp4/m4s

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serverHLS := &http.Server{
		Addr:    fmt.Sprintf(":%d", DefaultPort),
		Handler: muxHLS,
	}

	serverLLHLS := &http.Server{
		Addr:    fmt.Sprintf(":%d", LLHLSPort),
		Handler: muxLLHLS,
	}

	go func() {
		log.Printf("Starting HLS Station on port %d", DefaultPort)
		if err := serverHLS.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HLS Server failed: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting LLHLS Station on port %d", LLHLSPort)
		if err := serverLLHLS.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("LLHLS Server failed: %v", err)
		}
	}()

	<-sigChan
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamManager.stopFFmpeg()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		serverHLS.Shutdown(ctx)
	}()
	go func() {
		defer wg.Done()
		serverLLHLS.Shutdown(ctx)
	}()
	wg.Wait()
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

func (sm *StreamManager) calculateCurrentPosition() (seekTime float64, startNumberHLS, startNumberLLHLS int64) {
	now := time.Now().Unix()
	elapsed := now - sm.genesis.StartTime
	
	// Calculate seek position in current loop
	seekTime = float64(elapsed) 
	for seekTime >= sm.videoDuration {
		seekTime -= sm.videoDuration
	}
	
	// Calculate segment start numbers (never reset)
	startNumberHLS = elapsed / int64(SegmentDurationHLS)
	startNumberLLHLS = elapsed / int64(SegmentDurationLLHLS)
	
	return seekTime, startNumberHLS, startNumberLLHLS
}

func (sm *StreamManager) startFFmpeg() error {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if sm.isRunning {
		return nil
	}
	
	seekTime, startNumberHLS, startNumberLLHLS := sm.calculateCurrentPosition()
	sm.currentSeek = seekTime
	sm.startNumberHLS = startNumberHLS
	sm.startNumberLLHLS = startNumberLLHLS
	
	log.Printf("Starting FFmpeg at seek: %.2f, HLS segment: %d, LLHLS segment: %d", seekTime, startNumberHLS, startNumberLLHLS)
	
	// Clean up old segments in both directories
	filesHLS, _ := filepath.Glob(filepath.Join(OutputDirHLS, "*.ts"))
	for _, f := range filesHLS {
		os.Remove(f)
	}
	filesLLHLS, _ := filepath.Glob(filepath.Join(OutputDirLLHLS, "*.m4s")) // LLHLS uses m4s/mp4 usually, but let's stick to basic naming if possible or check args
	for _, f := range filesLLHLS {
		os.Remove(f)
	}
	// Also clean .mp4 init segments for fmp4 if present
	filesMp4, _ := filepath.Glob(filepath.Join(OutputDirLLHLS, "*.mp4"))
	for _, f := range filesMp4 {
		os.Remove(f)
	}
	
	// Build FFmpeg command with dual outputs
	args := []string{
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

		// Output 2: LLHLS (Low Latency - approximated with shorter segments and fmp4)
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", SegmentDurationLLHLS),
		"-hls_list_size", "10", // Keep more segments for LLHLS safety
		"-hls_flags", "delete_segments",
		"-hls_segment_type", "fmp4", // Fragmented MP4 for LLHLS
		"-start_number", fmt.Sprintf("%d", startNumberLLHLS),
		"-hls_segment_filename", filepath.Join(OutputDirLLHLS, "segment%d.m4s"),
		filepath.Join(OutputDirLLHLS, "stream.m3u8"),
	}
	
	sm.ffmpegCmd = exec.Command("ffmpeg", args...)
	sm.ffmpegCmd.Stdout = os.Stdout
	sm.ffmpegCmd.Stderr = os.Stderr
	
	if err := sm.ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	}
	
	sm.isRunning = true
	sm.updateLastAccess()
	
	// Wait for playlist to be generated (check HLS as primary)
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
	seekTime, _, _ := sm.calculateCurrentPosition()
	hours := int(seekTime) / 3600
	minutes := (int(seekTime) % 3600) / 60
	seconds := int(seekTime) % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}


func corsMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
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

func createPlaylistHandler(outputDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		playlistPath := filepath.Join(outputDir, "stream.m3u8")

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
}

func createSegmentHandler(outputDir string, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		segmentPath := filepath.Join(outputDir, segmentName)

		// Wait for segment to exist (max 5 seconds)
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(segmentPath); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, segmentPath)
	}
}




func createStreamHandler(outputDir string, contentType string, playlistAlias string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// path is already stripped of prefix by http.StripPrefix if used correctly
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
		streamManager.trackViewer(ip)

		// Start FFmpeg if not running
		// We check isRunning. Note: access to isRunning should probably be locked if it wasn't atomic/safe, 
		// but we follow the pattern in createPlaylistHandler.
		// Assuming isRunning is a field of StreamManager accessible here.
		if !streamManager.isRunning {
			if err := streamManager.startFFmpeg(); err != nil {
				log.Printf("Failed to start FFmpeg: %v", err)
				http.Error(w, "Failed to start stream", http.StatusInternalServerError)
				return
			}
		}

		streamManager.updateLastAccess()

		filePath := filepath.Join(outputDir, name)

		// Wait loop for file existence (especially if we just started FFmpeg)
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
