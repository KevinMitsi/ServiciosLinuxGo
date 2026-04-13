// File: internal/ws/hub.go
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/user/vm-manager/internal/ssh"
	"github.com/user/vm-manager/internal/systemd"
	"github.com/user/vm-manager/internal/vbox"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all connections
	},
}

type Hub struct {
	clients    map[*websocket.Conn]bool
	mutex      sync.Mutex
	sshClients map[string]*ssh.Client
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		sshClients: make(map[string]*ssh.Client),
	}
}

func (h *Hub) HandleTail(w http.ResponseWriter, r *http.Request) {
	vmName := r.URL.Query().Get("vm")
	filePath := r.URL.Query().Get("file")
	if vmName == "" || filePath == "" {
		http.Error(w, "Missing vm or file query parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close()

	h.mutex.Lock()
	h.clients[conn] = true
	h.mutex.Unlock()

	defer func() {
		h.mutex.Lock()
		delete(h.clients, conn)
		h.mutex.Unlock()
	}()

	sshClient, err := getSSHClientForVM(vmName)
	if err != nil {
		slog.Error("failed to get ssh client", "vm", vmName, "error", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outChan := make(chan string)
	go func() {
		for message := range outChan {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
				slog.Error("failed to write message to websocket", "error", err)
				cancel()
				return
			}
		}
	}()

	go func() {
		for {
			// Read messages from client (like close messages)
			if _, _, err := conn.NextReader(); err != nil {
				cancel()
				break
			}
		}
	}()

	cmd := "tail -f " + filePath
	if err := sshClient.StreamCommand(ctx, cmd, outChan); err != nil {
		slog.Error("streaming command failed", "error", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
	}
}

func (h *Hub) HandleStatus(w http.ResponseWriter, r *http.Request) {
	vmName := r.URL.Query().Get("vm")
	serviceName := r.URL.Query().Get("service")
	if vmName == "" || serviceName == "" {
		http.Error(w, "Missing vm or service query parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close()

	h.mutex.Lock()
	h.clients[conn] = true
	h.mutex.Unlock()

	defer func() {
		h.mutex.Lock()
		delete(h.clients, conn)
		h.mutex.Unlock()
	}()

	sshClient, err := getSSHClientForVM(vmName)
	if err != nil {
		slog.Error("failed to get ssh client", "vm", vmName, "error", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	serviceManager := systemd.NewServiceManager(sshClient)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				cancel()
				break
			}
		}
	}()

	for {
		select {
		case <-ticker.C:
			status, err := serviceManager.Status(serviceName)
			if err != nil {
				slog.Error("failed to get service status", "error", err)
				conn.WriteMessage(websocket.TextMessage, []byte("Error getting status: "+err.Error()))
				continue
			}
			jsonStatus, err := json.Marshal(status)
			if err != nil {
				slog.Error("failed to marshal status", "error", err)
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, jsonStatus); err != nil {
				slog.Error("failed to write status to websocket", "error", err)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func getSSHClientForVM(vmName string) (*ssh.Client, error) {
	registeredVMs, err := vbox.GetRegisteredVMs()
	if err != nil {
		return nil, fmt.Errorf("could not get registered VMs: %v", err)
	}

	var targetVM *vbox.VM
	for _, vm := range registeredVMs {
		if vm.Name == vmName {
			targetVM = &vm
			break
		}
	}

	if targetVM == nil {
		return nil, fmt.Errorf("vm %s not registered", vmName)
	}

	ip, err := vbox.GetVMIP(vmName)
	if err != nil {
		return nil, fmt.Errorf("could not get IP for VM %s: %v", vmName, err)
	}

	return ssh.GetClient(targetVM.SSHUser, ip, targetVM.SSHKeyPath, targetVM.SSHPort)
}
