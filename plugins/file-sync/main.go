package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Type    string                 `json:"type"`
	ID      string                 `json:"id,omitempty"`
	Action  string                 `json:"action,omitempty"`
	Event   string                 `json:"event,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
	Level   string                 `json:"level,omitempty"`
	Message string                 `json:"message,omitempty"`
}

type Config struct {
	SyncInterval       int      `json:"sync_interval"`
	ConflictResolution string   `json:"conflict_resolution"`
	WatchDirs          []string `json:"watch_dirs"`
}

type FileInfo struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	Hash       string    `json:"hash"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
}

func send(msg Message) {
	data, _ := json.Marshal(msg)
	fmt.Println(string(data))
}

func log(level, msg string) {
	send(Message{Type: "log", Level: level, Message: msg})
}

func request(action string, data map[string]interface{}) (map[string]interface{}, error) {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	send(Message{Type: "request", ID: id, Action: action, Data: data})
	return nil, nil
}

var (
	config      Config
	mu          sync.Mutex
	knownFiles  = make(map[string]FileInfo)
	syncRunning = false
)

func main() {
	log("info", "Starting file-sync plugin")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log("warn", "invalid message: "+err.Error())
			continue
		}
		handleMessage(msg)
	}
}

func handleMessage(msg Message) {
	switch msg.Type {
	case "init":
		if msg.Data != nil {
			if v, ok := msg.Data["config"].(map[string]interface{}); ok {
				data, _ := json.Marshal(v)
				json.Unmarshal(data, &config)
			}
		}
		if config.SyncInterval == 0 {
			config.SyncInterval = 30
		}
		if len(config.WatchDirs) == 0 {
			config.WatchDirs = []string{"sync"}
		}
		log("info", fmt.Sprintf("Config loaded: interval=%ds, dirs=%v", config.SyncInterval, config.WatchDirs))
		startSyncLoop()

	case "request":
		// Handle requests from Tash
		if msg.Action == "sync_now" {
			go runSync()
		} else if msg.Action == "get_status" {
			send(Message{
				Type: "response",
				ID:   msg.ID,
				Data: map[string]interface{}{
					"running":     syncRunning,
					"known_files": len(knownFiles),
					"watch_dirs":  config.WatchDirs,
				},
			})
		}

	case "event":
		if msg.Event == "nas_started" {
			go runSync()
		}
	}
}

func startSyncLoop() {
	if syncRunning {
		return
	}
	syncRunning = true
	go func() {
		ticker := time.NewTicker(time.Duration(config.SyncInterval) * time.Second)
		defer ticker.Stop()
		for syncRunning {
			select {
			case <-ticker.C:
				runSync()
			}
		}
	}()
	log("info", "Sync loop started")
}

func runSync() {
	mu.Lock()
	defer mu.Unlock()

	nasRoot := getNASRoot()
	if nasRoot == "" {
		log("warn", "NAS root not available")
		return
	}

	for _, dir := range config.WatchDirs {
		syncDir := filepath.Join(nasRoot, dir)
		if err := os.MkdirAll(syncDir, 0o755); err != nil {
			log("error", fmt.Sprintf("failed to create sync dir %s: %v", syncDir, err))
			continue
		}

		// Scan local files
		localFiles := scanDir(syncDir)
		for _, f := range localFiles {
			key := f.Path
			if existing, ok := knownFiles[key]; !ok || f.ModTime.After(existing.ModTime) {
				knownFiles[key] = f
				log("info", fmt.Sprintf("New/updated file: %s", f.Path))
			}
		}
	}

	// TODO: Push changes to other devices via NAS
	// For now, just log status
	log("info", fmt.Sprintf("Sync complete: %d files tracked", len(knownFiles)))
}

func scanDir(root string) []FileInfo {
	var files []FileInfo
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, FileInfo{
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	return files
}

func getNASRoot() string {
	// Request NAS root from Tash
	resp, _ := request("get_config", map[string]interface{}{"key": "nas.root"})
	if resp != nil {
		if v, ok := resp["value"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}