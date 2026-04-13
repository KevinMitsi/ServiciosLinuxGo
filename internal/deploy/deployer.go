// File: internal/deploy/deployer.go
package deploy

import (
	"archive/zip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

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
	// 1. Extract the .zip in a temporary local directory
	tempDir, err := ioutil.TempDir("", "vm-deploy-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := unzip(zipFile, tempDir); err != nil {
		return "", fmt.Errorf("failed to unzip file: %v", err)
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
	if _, _, err := d.SSHClient.RunCommand(fmt.Sprintf("mkdir -p %s", destFolder)); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %v", err)
	}

	// 4. Upload all files
	files, err := ioutil.ReadDir(tempDir)
	if err != nil {
		return "", fmt.Errorf("failed to read temp dir: %v", err)
	}

	for _, file := range files {
		localPath := filepath.Join(tempDir, file.Name())
		remotePath := filepath.Join(destFolder, file.Name())
		if err := d.SSHClient.UploadFile(localPath, remotePath); err != nil {
			return "", fmt.Errorf("failed to upload file %s: %v", file.Name(), err)
		}
	}

	// 5. Make scripts executable
	execCmd := fmt.Sprintf("chmod +x %s/run.sh && chmod +x %s/%s", destFolder, destFolder, mainBinary)
	if _, _, err := d.SSHClient.RunCommand(execCmd); err != nil {
		return "", fmt.Errorf("failed to make scripts executable: %v", err)
	}

	// 6. Return the full path of run.sh
	return filepath.Join(destFolder, "run.sh"), nil
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
