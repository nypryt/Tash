package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	// In a real implementation, you'd wait for response on stdin
	// For this example, we'll return empty
	return nil, nil
}

func main() {
	log("info", "Starting ssh-connect plugin")

	// Register command
	request("add_tab", map[string]interface{}{
		"id":   "ssh-connect",
		"name": "SSH",
		"kind": "tab",
	})

	// Keep running
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
		log("info", "Plugin initialized")
	case "request":
		// Handle requests from Tash
		if msg.Action == "ssh_connect" {
			deviceIP := msg.Data["device_ip"].(string)
			user := msg.Data["user"].(string)
			go sshConnect(deviceIP, user)
		}
	case "event":
		// Handle events
	}
}

func sshConnect(deviceIP, user string) {
	if user == "" {
		user = "root"
	}
	log("info", fmt.Sprintf("Connecting to %s@%s", user, deviceIP))

	cmd := exec.CommandContext(context.Background(), "ssh", fmt.Sprintf("%s@%s", user, deviceIP))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log("error", fmt.Sprintf("SSH failed: %v", err))
	}
}