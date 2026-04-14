// File: cmd/main.go
package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/user/vm-manager/internal/deploy"
	"github.com/user/vm-manager/internal/ssh"
	"github.com/user/vm-manager/internal/systemd"
	"github.com/user/vm-manager/internal/vbox"
	"github.com/user/vm-manager/internal/ws"
)

//go:embed web/*
var embeddedFS embed.FS

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/vms", handleGetRegisteredVMs)
	mux.HandleFunc("POST /api/vms", handleRegisterVM)
	mux.HandleFunc("/api/vms/discover", handleDiscoverVMs)
	mux.HandleFunc("/api/vms/{name}/info", handleVMInfo)
	mux.HandleFunc("POST /api/vms/{name}/start", handleStartVM)
	mux.HandleFunc("POST /api/vms/{name}/stop", handleStopVM)
	mux.HandleFunc("/api/deploy", handleDeploy)

	mux.HandleFunc("/api/service/create", handleServiceCreate)
	mux.HandleFunc("/api/service/start", handleServiceControl("start"))
	mux.HandleFunc("/api/service/stop", handleServiceControl("stop"))
	mux.HandleFunc("/api/service/restart", handleServiceControl("restart"))
	mux.HandleFunc("/api/service/enable", handleServiceControl("enable"))
	mux.HandleFunc("/api/service/disable", handleServiceControl("disable"))
	mux.HandleFunc("/api/service/status", handleServiceStatus)

	// WebSocket routes
	wsHub := ws.NewHub()
	mux.HandleFunc("/ws/tail", wsHub.HandleTail)
	mux.HandleFunc("/ws/status", wsHub.HandleStatus)

	// Static file server for frontend
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "index.html")
		} else {
			http.NotFound(w, r)
		}
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: loggingMiddleware(mux),
	}

	go func() {
		// Da tiempo mínimo al servidor antes de abrir el navegador.
		time.Sleep(400 * time.Millisecond)
		openBrowser("http://localhost:8080")
	}()

	slog.Info("Server starting", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		slog.Warn("Could not open browser automatically", "error", err, "url", url)
		return
	}

	slog.Info("Browser open requested", "url", url)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		slog.Info("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("Failed to write JSON response", "status", status, "error", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func handleGetRegisteredVMs(w http.ResponseWriter, r *http.Request) {
	slog.Info("Fetching registered VMs")
	vms, err := vbox.GetRegisteredVMs()
	if err != nil {
		slog.Error("Failed to fetch registered VMs", "error", err)
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("Registered VMs fetched", "count", len(vms))
	json.NewEncoder(w).Encode(vms)
}

func handleRegisterVM(w http.ResponseWriter, r *http.Request) {
	var vm vbox.VM
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			slog.Warn("Invalid multipart register VM request body", "error", err)
			writeJSONError(w, http.StatusBadRequest, "Invalid multipart form")
			return
		}

		vm.Name = r.FormValue("name")
		vm.SSHUser = r.FormValue("ssh_user")
		sshPort, err := strconv.Atoi(r.FormValue("ssh_port"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid ssh_port")
			return
		}
		vm.SSHPort = sshPort

		keyPath, err := saveUploadedSSHKey(r)
		if err != nil {
			slog.Error("Failed to save SSH key file", "error", err)
			writeJSONError(w, http.StatusBadRequest, "Failed to save SSH key file: "+err.Error())
			return
		}
		vm.SSHKeyPath = keyPath
	} else {
		if err := json.NewDecoder(r.Body).Decode(&vm); err != nil {
			slog.Warn("Invalid register VM request body", "error", err)
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	if vm.Name == "" || vm.SSHUser == "" || vm.SSHKeyPath == "" || vm.SSHPort <= 0 {
		writeJSONError(w, http.StatusBadRequest, "Missing required VM fields")
		return
	}

	slog.Info("Registering VM", "vm", vm.Name, "ssh_user", vm.SSHUser, "ssh_port", vm.SSHPort)
	if err := vbox.RegisterVM(vm.Name, vm.SSHUser, vm.SSHKeyPath, vm.SSHPort); err != nil {
		slog.Error("Failed to register VM", "vm", vm.Name, "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	started := true
	if err := vbox.StartVM(vm.Name); err != nil {
		started = false
		slog.Error("VM registered but failed to start automatically", "vm", vm.Name, "error", err)
	}
	slog.Info("VM registered", "vm", vm.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"message":      "VM registered",
		"ssh_key_path": vm.SSHKeyPath,
		"started":      started,
	})
}

func saveUploadedSSHKey(r *http.Request) (string, error) {
	file, header, err := r.FormFile("ssh_key_file")
	if err != nil {
		return "", fmt.Errorf("ssh_key_file is required")
	}
	defer file.Close()

	baseName := filepath.Base(header.Filename)
	if baseName == "." || baseName == string(filepath.Separator) || baseName == "" {
		return "", fmt.Errorf("invalid ssh key file name")
	}

	keysDir := filepath.Join("config", "keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return "", fmt.Errorf("could not create keys directory: %w", err)
	}

	destination := filepath.Join(keysDir, fmt.Sprintf("%d_%s", time.Now().Unix(), baseName))
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("could not create key file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		return "", fmt.Errorf("could not save key file: %w", err)
	}

	absPath, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("could not resolve key path: %w", err)
	}

	slog.Info("SSH key file saved", "file", absPath)
	return absPath, nil
}

func handleDiscoverVMs(w http.ResponseWriter, r *http.Request) {
	slog.Info("Discovering VMs from VBoxManage")
	vms, err := vbox.ListVMs()
	if err != nil {
		slog.Error("Failed to discover VBox VMs", "error", err)
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("VBox VMs discovered", "count", len(vms))
	json.NewEncoder(w).Encode(vms)
}

func handleVMInfo(w http.ResponseWriter, r *http.Request) {
	vmName := r.PathValue("name")
	slog.Info("Fetching VM info", "vm", vmName)
	info, err := vbox.GetVMInfo(vmName)
	if err != nil {
		slog.Error("Failed to fetch VM info", "vm", vmName, "error", err)
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	slog.Info("VM info fetched", "vm", vmName, "state", info.State, "ip", info.IP)
	json.NewEncoder(w).Encode(info)
}

func handleStartVM(w http.ResponseWriter, r *http.Request) {
	vmName := r.PathValue("name")
	if vmName == "" {
		writeJSONError(w, http.StatusBadRequest, "vm name is required")
		return
	}

	slog.Info("Start VM requested", "vm", vmName)
	ip, attempts, err := vbox.StartVMAndWaitForIP(vmName, 5*time.Second, 60)
	if err != nil {
		slog.Error("Start VM failed", "vm", vmName, "attempts", attempts, "error", err)
		writeJSONError(w, http.StatusGatewayTimeout, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":  "VM started",
		"vm":       vmName,
		"ip":       ip,
		"attempts": attempts,
	})
}

func handleStopVM(w http.ResponseWriter, r *http.Request) {
	vmName := r.PathValue("name")
	if vmName == "" {
		writeJSONError(w, http.StatusBadRequest, "vm name is required")
		return
	}

	slog.Info("Stop VM requested", "vm", vmName)
	if err := vbox.StopVM(vmName); err != nil {
		slog.Error("Stop VM failed", "vm", vmName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "VM stopped",
		"vm":      vmName,
	})
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
		writeJSONError(w, http.StatusBadRequest, "Failed to parse multipart form")
		return
	}

	file, header, err := r.FormFile("zip_file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "zip_file is required")
		return
	}
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeJSONError(w, http.StatusBadRequest, "Invalid file format: only .zip is supported. Please compress your app as .zip")
		file.Close()
		return
	}

	defer file.Close()

	tempFile, err := os.CreateTemp("", "upload-*.zip")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to create temp file")
		return
	}
	defer os.Remove(tempFile.Name())

	if _, err := io.Copy(tempFile, file); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to save uploaded file")
		return
	}

	vmName := r.FormValue("vm_name")
	destFolder := r.FormValue("dest_folder")
	execArgs := r.FormValue("exec_args")
	mainBinary := r.FormValue("main_binary")
	serviceName := r.FormValue("service_name")
	serviceDesc := r.FormValue("service_description")
	slog.Info("Starting deploy", "vm", vmName, "dest_folder", destFolder, "main_binary", mainBinary, "service", serviceName)

	sshClient, err := getSSHClientForVM(vmName)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	deployer := deploy.NewDeployer(sshClient)
	execPath, err := deployer.Deploy(tempFile.Name(), vmName, destFolder, execArgs, mainBinary)
	if err != nil {
		slog.Error("Deploy failed", "vm", vmName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "Deployment failed: "+err.Error())
		return
	}

	// Now create the systemd service
	unitCfg := systemd.ServiceConfig{
		Description: serviceDesc,
		ExecStart:   execPath,
		WorkDir:     path.Dir(execPath),
	}
	unitContent := systemd.GenerateUnitFile(unitCfg)
	serviceManager := systemd.NewServiceManager(sshClient)
	if err := serviceManager.Deploy(unitContent, serviceName); err != nil {
		slog.Error("Systemd deploy failed", "service", serviceName, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to deploy systemd service: "+err.Error())
		return
	}
	slog.Info("Deploy completed", "vm", vmName, "service", serviceName, "exec_path", execPath)

	writeJSON(w, http.StatusOK, map[string]string{
		"message":      "Deployment successful",
		"service_file": serviceName,
	})
}

func handleServiceCreate(w http.ResponseWriter, r *http.Request) {
	// This is now part of handleDeploy, but could be a standalone endpoint
	http.Error(w, `{"error": "Not implemented, use /api/deploy"}`, http.StatusNotImplemented)
}

func handleServiceControl(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VMName      string `json:"vm_name"`
			ServiceName string `json:"service_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Warn("Invalid service control request", "action", action, "error", err)
			http.Error(w, `{"error": "Invalid request"}`, http.StatusBadRequest)
			return
		}
		slog.Info("Service control requested", "action", action, "vm", req.VMName, "service", req.ServiceName)

		sshClient, err := getSSHClientForVM(req.VMName)
		if err != nil {
			http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		manager := systemd.NewServiceManager(sshClient)

		var serviceErr error
		switch action {
		case "start":
			serviceErr = manager.Start(req.ServiceName)
		case "stop":
			serviceErr = manager.Stop(req.ServiceName)
		case "restart":
			serviceErr = manager.Restart(req.ServiceName)
		case "enable":
			serviceErr = manager.Enable(req.ServiceName)
		case "disable":
			serviceErr = manager.Disable(req.ServiceName)
		default:
			http.Error(w, `{"error": "Invalid action"}`, http.StatusBadRequest)
			return
		}

		if serviceErr != nil {
			slog.Error("Service control failed", "action", action, "vm", req.VMName, "service", req.ServiceName, "error", serviceErr)
			http.Error(w, `{"error": "`+serviceErr.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		slog.Info("Service control completed", "action", action, "vm", req.VMName, "service", req.ServiceName)
		w.WriteHeader(http.StatusOK)
	}
}

func handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	vmName := r.URL.Query().Get("vm")
	serviceName := r.URL.Query().Get("service")
	slog.Info("Service status requested", "vm", vmName, "service", serviceName)

	sshClient, err := getSSHClientForVM(vmName)
	if err != nil {
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	manager := systemd.NewServiceManager(sshClient)
	status, err := manager.Status(serviceName)
	if err != nil {
		slog.Error("Service status failed", "vm", vmName, "service", serviceName, "error", err)
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("Service status fetched", "vm", vmName, "service", serviceName, "active", status.ActiveState, "sub", status.SubState)
	json.NewEncoder(w).Encode(status)
}

func getSSHClientForVM(vmName string) (*ssh.Client, error) {
	registeredVMs, err := vbox.GetRegisteredVMs()
	if err != nil {
		return nil, fmt.Errorf("could not get registered VMs: %v", err)
	}

	var targetVM *vbox.VM
	for i := range registeredVMs {
		if registeredVMs[i].Name == vmName {
			targetVM = &registeredVMs[i]
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
