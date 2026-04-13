// File: internal/deploy/deployer.go
package deploy

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/user/vm-manager/internal/ssh"
)

// Deployer handles the deployment process.
type Deployer struct {
	SSHClient *ssh.Client
}

// NewDeployer creates a new Deployer.
func NewDeployer(client *ssh.Client) *Deployer {
	return &Deployer{SSHClient: client}
}

// Deploy unzips a file, creates a run script, and uploads everything.
func (d *Deployer) Deploy(zipFile, vmName, destFolder, execArgs, mainBinary string) (string, error) {
	remoteDest := normalizeRemoteDir(destFolder)
	if remoteDest == "" {
		return "", fmt.Errorf("destination folder is required")
	}

	// 1. Extract the .zip in a temporary local directory
	tempDir, err := ioutil.TempDir("", "vm-deploy-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := validateZipSignature(zipFile); err != nil {
		return "", err
	}

	if err := unzip(zipFile, tempDir); err != nil {
		return "", fmt.Errorf("failed to unzip file: %v. Only valid .zip files are supported", err)
	}

	// 2. Create the wrapper script
	runShPath := filepath.Join(tempDir, "run.sh")
	runShContent := `#!/bin/bash
# run.sh — generado automáticamente
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_FILE="$SCRIPT_DIR/execution_log.txt"

# Obtener el siguiente número incremental
if [ -f "$LOG_FILE" ]; then
    LAST=$(tail -1 "$LOG_FILE" | awk -F' - ' '{print $1}')
    COUNT=$((LAST + 1))
else
    COUNT=1
fi

TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
echo "$COUNT - $TIMESTAMP" >> "$LOG_FILE"

# Ejecutar la aplicación real
exec "$SCRIPT_DIR/` + mainBinary + `" ` + execArgs + `
`
	if err := ioutil.WriteFile(runShPath, []byte(runShContent), 0755); err != nil {
		return "", fmt.Errorf("failed to create run.sh: %v", err)
	}

	// 3. Create remote directory
	if _, _, err := d.SSHClient.RunCommand(fmt.Sprintf("mkdir -p %q", remoteDest)); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %v", err)
	}

	// 4. Upload all files
	files, err := ioutil.ReadDir(tempDir)
	if err != nil {
		return "", fmt.Errorf("failed to read temp dir: %v", err)
	}

	for _, file := range files {
		localPath := filepath.Join(tempDir, file.Name())
		remotePath := path.Join(remoteDest, file.Name())
		if err := d.SSHClient.UploadFile(localPath, remotePath); err != nil {
			return "", fmt.Errorf("failed to upload file %s: %v", file.Name(), err)
		}
	}

	// 5. Make scripts executable
	runShRemote := path.Join(remoteDest, "run.sh")
	mainBinaryRemote := path.Join(remoteDest, mainBinary)
	execCmd := fmt.Sprintf("chmod +x %q && chmod +x %q", runShRemote, mainBinaryRemote)
	if _, _, err := d.SSHClient.RunCommand(execCmd); err != nil {
		return "", fmt.Errorf("failed to make scripts executable: %v", err)
	}

	// 6. Return the full path of run.sh
	return runShRemote, nil
}

func normalizeRemoteDir(dest string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(dest, "\\", "/"))
	if cleaned == "" {
		return ""
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return path.Clean(cleaned)
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

func validateZipSignature(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open uploaded file: %v", err)
	}
	defer f.Close()

	header := make([]byte, 4)
	n, err := io.ReadFull(f, header)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("uploaded file is too small to be a valid zip")
		}
		return fmt.Errorf("failed to read uploaded file header: %v", err)
	}
	if n < 4 {
		return fmt.Errorf("uploaded file is too small to be a valid zip")
	}

	if header[0] != 'P' || header[1] != 'K' {
		return fmt.Errorf("invalid file format: expected ZIP signature (PK)")
	}

	return nil
}
