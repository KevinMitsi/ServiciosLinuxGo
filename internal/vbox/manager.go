// File: internal/vbox/manager.go
package vbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type VM struct {
	Name       string `json:"name"`
	SSHUser    string `json:"ssh_user"`
	SSHKeyPath string `json:"ssh_key_path"`
	SSHPort    int    `json:"ssh_port"`
}

type VMInfo struct {
	Name  string `json:"name"`
	State string `json:"state"`
	IP    string `json:"ip"`
}

var (
	vmsFilePath = filepath.Join("config", "vms.json")
	vmsMutex    = &sync.RWMutex{}
)

// ListVMs lista todas las VMs registradas en VirtualBox.
func ListVMs() ([]VM, error) {
	slog.Info("VBox list vms started")
	out, err := runVBoxCommand("list", "vms")
	if err != nil {
		slog.Error("VBox list vms failed", "error", err)
		return nil, fmt.Errorf("failed to list vms: %v", err)
	}

	var vms []VM
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		firstQuote := strings.Index(line, "\"")
		lastQuote := strings.LastIndex(line, "\"")
		if firstQuote < 0 || lastQuote <= firstQuote {
			slog.Warn("Could not parse VBox VM line", "line", line)
			continue
		}

		name := line[firstQuote+1 : lastQuote]
		vms = append(vms, VM{Name: name})
		slog.Debug("VBox VM discovered", "name", name)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan VBox list output: %v", err)
	}

	slog.Info("VBox list vms completed", "count", len(vms))
	return vms, nil
}

// GetVMInfo retorna info de una VM: estado (running/stopped), IP (guestproperty).
func GetVMInfo(vmName string) (VMInfo, error) {
	slog.Info("VBox showvminfo started", "vm", vmName)
	out, err := runVBoxCommand("showvminfo", vmName, "--machinereadable")
	if err != nil {
		slog.Error("VBox showvminfo failed", "vm", vmName, "error", err)
		return VMInfo{}, fmt.Errorf("failed to get vm info for %s: %v", vmName, err)
	}

	info := VMInfo{Name: vmName}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VMState=") {
			info.State = strings.Trim(strings.Split(line, "=")[1], "\"")
		}
	}

	ip, err := GetVMIP(vmName)
	if err != nil {
		// Not a fatal error if IP is not found
		info.IP = "N/A"
	} else {
		info.IP = ip
	}

	slog.Info("VBox showvminfo completed", "vm", vmName, "state", info.State, "ip", info.IP)

	return info, nil
}

// RegisterVM registra una VM en el sistema de gestión (persiste en vms.json).
func RegisterVM(vmName, sshUser, sshKeyPath string, sshPort int) error {
	vmsMutex.Lock()
	defer vmsMutex.Unlock()
	slog.Info("Register VM requested", "vm", vmName, "ssh_user", sshUser, "ssh_port", sshPort)

	// Validate that the VM exists in VirtualBox
	allVMs, err := ListVMs()
	if err != nil {
		return err
	}
	found := false
	for _, vm := range allVMs {
		if vm.Name == vmName {
			found = true
			break
		}
	}
	if !found {
		slog.Warn("VM not found in VBox during register", "vm", vmName)
		return fmt.Errorf("vm %s not found in VirtualBox", vmName)
	}

	file, err := ioutil.ReadFile(vmsFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read vms file: %v", err)
	}

	var data struct {
		VMs []VM `json:"vms"`
	}
	if len(file) > 0 {
		if err := json.Unmarshal(file, &data); err != nil {
			return fmt.Errorf("failed to unmarshal vms file: %v", err)
		}
	}

	newVM := VM{
		Name:       vmName,
		SSHUser:    sshUser,
		SSHKeyPath: sshKeyPath,
		SSHPort:    sshPort,
	}

	data.VMs = append(data.VMs, newVM)

	newFile, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return fmt.Errorf("failed to marshal vms: %v", err)
	}

	if err := ioutil.WriteFile(vmsFilePath, newFile, 0644); err != nil {
		return err
	}

	slog.Info("VM stored in config", "vm", vmName, "file", vmsFilePath)
	return nil
}

// StartVM enciende una VM en VirtualBox (si ya esta running, no hace nada).
func StartVM(vmName string) error {
	slog.Info("VBox start VM requested", "vm", vmName)

	info, err := GetVMInfo(vmName)
	if err == nil && strings.EqualFold(info.State, "running") {
		slog.Info("VBox VM already running", "vm", vmName)
		return nil
	}

	if _, err := runVBoxCommand("startvm", vmName, "--type", "headless"); err != nil {
		slog.Error("VBox start VM failed", "vm", vmName, "error", err)
		return fmt.Errorf("failed to start vm %s: %v", vmName, err)
	}

	slog.Info("VBox VM started", "vm", vmName)
	return nil
}

// StopVM apaga una VM en VirtualBox (si no esta running, no hace nada).
func StopVM(vmName string) error {
	slog.Info("VBox stop VM requested", "vm", vmName)

	info, err := GetVMInfo(vmName)
	if err == nil && !strings.EqualFold(info.State, "running") {
		slog.Info("VBox VM already stopped", "vm", vmName, "state", info.State)
		return nil
	}

	if _, err := runVBoxCommand("controlvm", vmName, "poweroff"); err != nil {
		slog.Error("VBox stop VM failed", "vm", vmName, "error", err)
		return fmt.Errorf("failed to stop vm %s: %v", vmName, err)
	}

	slog.Info("VBox VM stopped", "vm", vmName)
	return nil
}

// GetVMIP obtiene la IP de la VM via VBoxManage.
func GetVMIP(vmName string) (string, error) {
	slog.Info("VBox guestproperty get started", "vm", vmName)
	out, err := runVBoxCommand("guestproperty", "get", vmName, "/VirtualBox/GuestInfo/Net/0/V4/IP")
	if err != nil {
		slog.Error("VBox guestproperty get failed", "vm", vmName, "error", err)
		return "", fmt.Errorf("failed to get vm ip for %s: %v", vmName, err)
	}

	output := string(out)
	if strings.HasPrefix(output, "Value: ") {
		ip := strings.TrimSpace(strings.TrimPrefix(output, "Value: "))
		slog.Info("VBox guestproperty get completed", "vm", vmName, "ip", ip)
		return ip, nil
	}

	return "", fmt.Errorf("could not parse ip from VBoxManage output: %s", output)
}

// GetRegisteredVMs returns the list of VMs from vms.json
func GetRegisteredVMs() ([]VM, error) {
	vmsMutex.RLock()
	defer vmsMutex.RUnlock()

	file, err := ioutil.ReadFile(vmsFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []VM{}, nil
		}
		return nil, fmt.Errorf("failed to read vms file: %v", err)
	}

	var data struct {
		VMs []VM `json:"vms"`
	}
	if len(file) > 0 {
		if err := json.Unmarshal(file, &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal vms file: %v", err)
		}
	}
	slog.Info("Registered VMs loaded from file", "count", len(data.VMs), "file", vmsFilePath)
	return data.VMs, nil
}

func runVBoxCommand(args ...string) ([]byte, error) {
	cmd := exec.Command("VBoxManage", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			trimmed = "no output"
		}
		return nil, fmt.Errorf("VBoxManage %s failed: %w (output: %s)", strings.Join(args, " "), err, trimmed)
	}

	return out, nil
}
