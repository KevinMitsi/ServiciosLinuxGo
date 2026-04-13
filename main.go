// File: cmd/main.go
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if err := json.NewDecoder(r.Body).Decode(&vm); err != nil {
		slog.Warn("Invalid register VM request body", "error", err)
		http.Error(w, `{"error": "Invalid request body"}`, http.StatusBadRequest)
		return
	}
	slog.Info("Registering VM", "vm", vm.Name, "ssh_user", vm.SSHUser, "ssh_port", vm.SSHPort)
	if err := vbox.RegisterVM(vm.Name, vm.SSHUser, vm.SSHKeyPath, vm.SSHPort); err != nil {
		slog.Error("Failed to register VM", "vm", vm.Name, "error", err)
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("VM registered", "vm", vm.Name)
	w.WriteHeader(http.StatusCreated)
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

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
		http.Error(w, `{"error": "Failed to parse multipart form"}`, http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("zip_file")
	if err != nil {
		http.Error(w, `{"error": "zip_file is required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	tempFile, err := os.CreateTemp("", "upload-*.zip")
	if err != nil {
		http.Error(w, `{"error": "Failed to create temp file"}`, http.StatusInternalServerError)
		return
	}
	defer os.Remove(tempFile.Name())

	if _, err := io.Copy(tempFile, file); err != nil {
		http.Error(w, `{"error": "Failed to save uploaded file"}`, http.StatusInternalServerError)
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
		http.Error(w, `{"error": "`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	deployer := deploy.NewDeployer(sshClient)
	execPath, err := deployer.Deploy(tempFile.Name(), vmName, destFolder, execArgs, mainBinary)
	if err != nil {
		slog.Error("Deploy failed", "vm", vmName, "error", err)
		http.Error(w, `{"error": "Deployment failed: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	// Now create the systemd service
	unitCfg := systemd.ServiceConfig{
		Description: serviceDesc,
		ExecStart:   execPath,
		WorkDir:     filepath.Dir(execPath),
	}
	unitContent := systemd.GenerateUnitFile(unitCfg)
	serviceManager := systemd.NewServiceManager(sshClient)
	if err := serviceManager.Deploy(unitContent, serviceName); err != nil {
		slog.Error("Systemd deploy failed", "service", serviceName, "error", err)
		http.Error(w, `{"error": "Failed to deploy systemd service: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	slog.Info("Deploy completed", "vm", vmName, "service", serviceName, "exec_path", execPath)

	fmt.Fprintf(w, `{"message": "Deployment successful", "service_file": "%s"}`, serviceName)
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
