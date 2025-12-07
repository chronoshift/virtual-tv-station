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

// Genesis represents the station's start time and state
type Genesis struct {
	StartTime      int64   `json:"start_time"`
	IsPaused       bool    `json:"is_paused"`
	PausedPosition float64 `json:"paused_position"`
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

	// CPU tracking
	prevIdleTime   uint64
	prevTotalTime  uint64
	cachedCPUUsage string
}

// Stats response for the dashboard
type Stats struct {
	Status         string   `json:"status"`
	ViewerCount    int      `json:"viewer_count"`
	ViewersHLS     []string `json:"viewers_hls"`
	ViewersLLHLS   []string `json:"viewers_llhls"`
	CurrentPlaying string   `json:"current_playing"`
	IsRunning      bool     `json:"is_running"`
	IsPaused       bool     `json:"is_paused"`
	CPUUsage       string   `json:"cpu_usage"` // Placeholder
	Progress       float64  `json:"progress"`
	VideoDuration  float64  `json:"video_duration"`
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
	go streamManager.monitorStreamHealth()
	go streamManager.cleanupViewers()
	go streamManager.updateCPUStats()

	// HLS Server
	muxHLS := http.NewServeMux()
	muxHLS.HandleFunc("/", corsMiddleware(http.HandlerFunc(handleDashboard)))
	muxHLS.Handle("/hls/", http.StripPrefix("/hls", corsMiddleware(createStreamHandler(OutputDirHLS, "video/MP2T", "stream.m3u8", "hls"))))
	muxHLS.HandleFunc("/api/stats", corsMiddleware(http.HandlerFunc(handleStats)))
	muxHLS.HandleFunc("/api/control", corsMiddleware(http.HandlerFunc(handleControl)))

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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	if streamManager.genesis.IsPaused {
		status = "Paused"
	} else if streamManager.isRunning {
		status = "Broadcasting"
	}

	stats := Stats{
		Status:         status,
		ViewerCount:    total,
		ViewersHLS:     viewersHLS,
		ViewersLLHLS:   viewersLLHLS,
		CurrentPlaying: streamManager.getCurrentPlayingTime(),
		IsRunning:      streamManager.isRunning,
		IsPaused:       streamManager.genesis.IsPaused,
		CPUUsage:       streamManager.getCachedCPU(),
		Progress:       streamManager.getProgress(),
		VideoDuration:  streamManager.videoDuration,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := r.URL.Query().Get("action")
	
	switch action {
	case "pause":
		streamManager.pauseStream()
	case "resume":
		streamManager.resumeStream()
	case "seek":
		posStr := r.URL.Query().Get("position")
		pos, err := strconv.ParseFloat(posStr, 64)
		if err != nil {
			http.Error(w, "Invalid position", http.StatusBadRequest)
			return
		}
		streamManager.seekStream(pos)
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusOK)
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
		if strings.Contains(ip, ",") {
			ip = strings.TrimSpace(strings.Split(ip, ",")[0])
		}
		
		streamManager.trackViewer(ip, streamType)

		// Start FFmpeg if not running and NOT PAUSED
		// Check IsPaused atomically? No, just use the struct.
		// Data race potential here but acceptable for prototype.
		if !streamManager.genesis.IsPaused {
			if !streamManager.isRunning {
				if err := streamManager.startFFmpeg(); err != nil {
					log.Printf("Failed to start FFmpeg: %v", err)
					http.Error(w, "Failed to start stream", http.StatusInternalServerError)
					return
				}
			}
			streamManager.updateLastAccess()
		}

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
	sm.saveGenesis()
	log.Printf("Created new genesis time: %s", time.Unix(sm.genesis.StartTime, 0))
	return nil
}

func (sm *StreamManager) saveGenesis() {
	data, _ := json.Marshal(sm.genesis)
	os.WriteFile("genesis.json", data, 0644)
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
	// If paused, use the paused position
	if sm.genesis.IsPaused {
		seekTime = sm.genesis.PausedPosition
	} else {
		now := time.Now().Unix()
		elapsed := float64(now - sm.genesis.StartTime)
		
		// Calculate position in loop
		seekTime = elapsed
		for seekTime >= sm.videoDuration {
			seekTime -= sm.videoDuration
		}
	}
	
	// Calculate monotonic start numbers
	// For HLS monotonic, we use elapsed time even if looped/seeked?
	// If we seek, we might break continuity for existing players.
	// Ideally, start number increments.
	// For now, we derive from seekTime (which resets on loop).
	// This might cause player glitch on loop, but fine for seeking.
	// Better: use wall clock for monotonicity if possible, but content changes.
	// We'll use seekTime logic for simplicity.
	
	startNumberHLS = int64(seekTime / float64(SegmentDurationHLS))
	startNumberLLHLS = int64(seekTime / float64(SegmentDurationLLHLS))
	
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

	// Write overlay text file to avoid command line escaping issues
	overlayContent := fmt.Sprintf("%%{eif:100*(t+%.2f)/%.2f:d}%%", seekTime, sm.videoDuration)
	os.WriteFile("overlay.txt", []byte(overlayContent), 0644)

	vf := "drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:textfile=overlay.txt:reload=1:x=w-tw-10:y=h-th-10:fontsize=48:fontcolor=green:box=1:boxcolor=black@0.5"
	log.Printf("FFmpeg Filter: %s", vf)

	args := []string{
		"-re",
		"-ss", fmt.Sprintf("%.2f", seekTime),
		"-i", VideoPath,
		"-vf", vf,
		"-c:v", "h264_nvenc",
		"-tune", "ll",
		"-preset", "fast",
		"-cq", "26",
		"-c:a", "copy",
		
		// Output 1: HLS (Standard)
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", SegmentDurationHLS),
		"-hls_list_size", "5",
		"-hls_flags", "delete_segments",
		"-start_number", fmt.Sprintf("%d", startNumberHLS),
		"-hls_segment_filename", filepath.Join(OutputDirHLS, "segment%d.ts"),
		filepath.Join(OutputDirHLS, "stream.m3u8"),

		// Output 2: LLHLS (Low Latency - approximated)
		"-c:v", "h264_nvenc",
		"-tune", "ll",
		"-preset", "fast",
		"-cq", "26",
		"-c:a", "copy",
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
		log.Fatalf("Failed to start FFmpeg (fatal error, crashing to trigger restart): %v", err)
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	}
	
	// Reaper
	go func() {
		err := sm.ffmpegCmd.Wait()
		
		sm.ffmpegMutex.Lock()
		shouldBeRunning := sm.isRunning
		sm.ffmpegMutex.Unlock()
		
		if shouldBeRunning && err != nil {
			log.Fatalf("FFmpeg exited unexpectedly while stream should be running: %v. Crashing to trigger restart.", err)
		} else if shouldBeRunning {
			log.Printf("FFmpeg exited cleanly but unexpectedly. Crashing to trigger restart.")
			os.Exit(1)
		}
	}()
	
	sm.isRunning = true
	sm.lastAccess = time.Now()
	
	// Wait for playlist
	playlistCreated := false
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(OutputDirHLS, "stream.m3u8")); err == nil {
			playlistCreated = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	
	if !playlistCreated {
		return fmt.Errorf("timeout waiting for playlist creation")
	}
	
	return nil
}

func (sm *StreamManager) monitorStreamHealth() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		sm.ffmpegMutex.Lock()
		running := sm.isRunning
		sm.ffmpegMutex.Unlock()

		if !running {
			continue
		}

		info, err := os.Stat(filepath.Join(OutputDirHLS, "stream.m3u8"))
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("Warning: Stream running but playlist missing")
			}
			continue
		}

		if time.Since(info.ModTime()) > 30*time.Second {
			log.Fatalf("Stream stalled: playlist not updated in %v. Crashing to trigger restart.", time.Since(info.ModTime()))
		}
	}
}

func (sm *StreamManager) stopFFmpeg() {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	
	if sm.ffmpegCmd != nil && sm.ffmpegCmd.Process != nil {
		sm.ffmpegCmd.Process.Kill()
	}
	sm.isRunning = false
}

func (sm *StreamManager) pauseStream() {
	sm.stopFFmpeg()
	
	// Calculate where we were
	seekTime, _, _ := sm.calculateCurrentPosition()
	
	sm.genesis.IsPaused = true
	sm.genesis.PausedPosition = seekTime
	sm.saveGenesis()
}

func (sm *StreamManager) resumeStream() {
	// Calculate new start time to resume from paused position
	// CurrentTime = Now - StartTime
	// We want CurrentTime = PausedPosition
	// So StartTime = Now - PausedPosition
	
	sm.genesis.StartTime = time.Now().Unix() - int64(sm.genesis.PausedPosition)
	sm.genesis.IsPaused = false
	sm.saveGenesis()
	
	// Force start immediately
	sm.startFFmpeg()
}

func (sm *StreamManager) seekStream(pos float64) {
	sm.stopFFmpeg()
	
	// Update StartTime so that current time matches pos
	sm.genesis.StartTime = time.Now().Unix() - int64(pos)
	sm.genesis.PausedPosition = pos // Update this just in case we stay paused?
	// If we are paused, we stay paused but at new position.
	// If playing, we resume from new position.
	// Let's check state.
	if sm.genesis.IsPaused {
		sm.genesis.PausedPosition = pos
	} else {
		sm.genesis.StartTime = time.Now().Unix() - int64(pos)
	}
	sm.saveGenesis()
	
	if !sm.genesis.IsPaused {
		sm.startFFmpeg()
	}
}

func (sm *StreamManager) updateLastAccess() {
	sm.ffmpegMutex.Lock()
	sm.lastAccess = time.Now()
	sm.ffmpegMutex.Unlock()
}

func (sm *StreamManager) watchdog() {
	for {
		time.Sleep(5 * time.Second)
		// Don't stop if paused (it's already stopped)
		// Only stop if running and idle
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
	seekTime, _, _ := sm.calculateCurrentPosition()
	
	hours := int(seekTime / 3600)
	minutes := int((seekTime - float64(hours*3600)) / 60)
	seconds := int(seekTime - float64(hours*3600) - float64(minutes*60))
	
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func (sm *StreamManager) getProgress() float64 {
	seekTime, _, _ := sm.calculateCurrentPosition()
	
	if sm.videoDuration > 0 {
		return (seekTime / sm.videoDuration) * 100
	}
	return 0
}

func (sm *StreamManager) getCPUSample() (idle, total uint64, err error) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return
	}
	lines := strings.Split(string(contents), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "cpu" {
			numFields := len(fields)
			for i := 1; i < numFields; i++ {
				val, _ := strconv.ParseUint(fields[i], 10, 64)
				total += val
				if i == 4 { // idle is the 5th field (index 4)
					idle = val
				}
			}
			return
		}
	}
	return
}

func (sm *StreamManager) getCPUUsage() string {
	idle, total, err := sm.getCPUSample()
	if err != nil {
		return "0%"
	}

	diffIdle := float64(idle - sm.prevIdleTime)
	diffTotal := float64(total - sm.prevTotalTime)

	sm.prevIdleTime = idle
	sm.prevTotalTime = total

	if diffTotal > 0 {
		cpu := (1.0 - diffIdle/diffTotal) * 100.0
		return fmt.Sprintf("%.1f%%", cpu)
	}
	return "0%"
}

func (sm *StreamManager) updateCPUStats() {
	ticker := time.NewTicker(2 * time.Second)
	for range ticker.C {
		usage := sm.getCPUUsage()
		sm.ffmpegMutex.Lock()
		sm.cachedCPUUsage = usage
		sm.ffmpegMutex.Unlock()
	}
}

func (sm *StreamManager) getCachedCPU() string {
	sm.ffmpegMutex.Lock()
	defer sm.ffmpegMutex.Unlock()
	if sm.cachedCPUUsage == "" {
		return "0%"
	}
	return sm.cachedCPUUsage
}
