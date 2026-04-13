// File: internal/systemd/service.go
package systemd

import (
	"fmt"
	"strings"

	"github.com/user/vm-manager/internal/ssh"
)

type ServiceConfig struct {
	Description string
	ExecStart   string
	WorkDir     string
}

type ServiceStatus struct {
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	Description string `json:"description"`
	Since       string `json:"since"`
	RawOutput   string `json:"raw_output"`
}

type ServiceManager struct {
	SSHClient *ssh.Client
}

func NewServiceManager(client *ssh.Client) *ServiceManager {
	return &ServiceManager{SSHClient: client}
}

func GenerateUnitFile(cfg ServiceConfig) string {
	return fmt.Sprintf(`[Unit]
Description=%s
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=2s
WorkingDirectory=%s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, cfg.Description, cfg.ExecStart, cfg.WorkDir)
}

func (s *ServiceManager) Deploy(unitContent, unitFileName string) error {
	remotePath := fmt.Sprintf("/etc/systemd/system/%s", unitFileName)
	cmd := fmt.Sprintf("echo '%s' | sudo tee %s", unitContent, remotePath)
	_, _, err := s.SSHClient.RunCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to write unit file: %v", err)
	}

	_, _, err = s.SSHClient.RunCommand("sudo systemctl daemon-reload")
	if err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %v", err)
	}
	return nil
}

func (s *ServiceManager) Start(unitName string) error {
	_, _, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl start %s", unitName))
	return err
}

func (s *ServiceManager) Stop(unitName string) error {
	_, _, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl stop %s", unitName))
	return err
}

func (s *ServiceManager) Restart(unitName string) error {
	_, _, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl restart %s", unitName))
	return err
}

func (s *ServiceManager) Enable(unitName string) error {
	_, _, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl enable %s", unitName))
	return err
}

func (s *ServiceManager) Disable(unitName string) error {
	_, _, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl disable %s", unitName))
	return err
}

func (s *ServiceManager) Status(unitName string) (ServiceStatus, error) {
	stdout, stderr, err := s.SSHClient.RunCommand(fmt.Sprintf("sudo systemctl status %s --no-pager", unitName))
	if err != nil && !isAllowedSystemctlStatusExit(err) {
		return ServiceStatus{}, err
	}

	rawOutput := stdout
	if strings.TrimSpace(rawOutput) == "" {
		rawOutput = stderr
	}
	status := ServiceStatus{RawOutput: rawOutput}

	if strings.Contains(rawOutput, "could not be found") || strings.Contains(rawOutput, "not-found") {
		status.ActiveState = "not-found"
		status.SubState = "not-found"
		return status, nil
	}

	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "Loaded: ") {
			parts := strings.Split(trimmedLine, ";")
			if len(parts) > 1 {
				status.Description = strings.TrimSpace(parts[0])
			}
		} else if strings.HasPrefix(trimmedLine, "Active: ") {
			parts := strings.Fields(trimmedLine)
			if len(parts) > 1 {
				status.ActiveState = parts[1]
			}
			if len(parts) > 2 {
				status.SubState = strings.Trim(parts[2], "()")
			}
			if strings.Contains(trimmedLine, "since") {
				sinceParts := strings.Split(trimmedLine, "since")
				if len(sinceParts) > 1 {
					status.Since = strings.TrimSpace(sinceParts[1])
				}
			}
		}
	}

	return status, nil
}

func isAllowedSystemctlStatusExit(err error) bool {
	errText := err.Error()
	return strings.Contains(errText, "status 3") ||
		strings.Contains(errText, "status 4") ||
		strings.Contains(errText, "exit status 3") ||
		strings.Contains(errText, "exit status 4")
}
