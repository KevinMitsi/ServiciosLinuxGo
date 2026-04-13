# VM Deployment & Management Automator

This is a Go-based web platform to automate deploying and managing applications on VirtualBox VMs using systemd.

## Features

- **VM Management**: Register VirtualBox VMs and view their status.
- **Automated Deployment**: Upload a `.zip` archive, and the tool will unpack it, create a run script, and transfer it to the VM.
- **Systemd Integration**: Automatically generates and manages `systemd` service units for your applications.
- **Real-time Monitoring**: Live `tail -f` of log files and real-time service status polling via WebSockets.
- **Single Binary**: The entire application, including the frontend, is compiled into a single, self-contained binary.

## Tech Stack

- **Backend**: Go (`net/http`, `gorilla/websocket`, `golang.org/x/crypto/ssh`)
- **Frontend**: Vanilla HTML, CSS, and JavaScript (embedded)
- **VM Control**: `VBoxManage` command-line tool
- **Remote Execution**: SSH

## Project Structure

```
/cmd/main.go                    # Entrypoint and API handlers
/internal/
  vbox/manager.go               # VBoxManage wrapper
  ssh/client.go                 # SSH client pool
  deploy/deployer.go            # Deployment logic (.zip handling)
  systemd/service.go            # Systemd unit file generation and control
  ws/hub.go                     # WebSocket hub for real-time updates
/web/
  templates/index.html          # Single Page Application UI
  static/app.js                 # Frontend JavaScript
  static/style.css              # Frontend CSS
/config/
  vms.json                      # Persistence for registered VMs
/go.mod
/README.md
```

## Prerequisites

1.  **Go**: Version 1.21 or later.
2.  **VirtualBox**: Must be installed and `VBoxManage` must be in your system's PATH.
3.  **Target VMs**:
    *   Linux-based.
    *   SSH server installed and running.
    *   VirtualBox Guest Additions installed (for IP address retrieval).
    *   A user account accessible via SSH with key-based authentication.
    *   `sudo` access for that user to manage systemd services without a password.

## Setup

### 1. Sudoers Configuration on Target VMs

For the application to manage `systemd` services, the SSH user needs passwordless `sudo` access for specific commands.

Log into each target VM and add the following line to the sudoers file by running `sudo visudo`. Replace `your_ssh_user` with the actual username.

```
your_ssh_user ALL=(ALL) NOPASSWD: /bin/systemctl start *, /bin/systemctl stop *, /bin/systemctl restart *, /bin/systemctl enable *, /bin/systemctl disable *, /bin/systemctl daemon-reload, /usr/bin/tee /etc/systemd/system/*
```

**WARNING**: This configuration allows the specified user to run several `systemctl` commands as root without a password. Only use this in a trusted development environment.

### 2. Compilation

Navigate to the project's root directory and run the build command:

```sh
go build -o vm-manager ./cmd/main.go
```

This will create a single executable file named `vm-manager`.

### 3. Running the Application

Execute the compiled binary:

```sh
./vm-manager
```

The web interface will be available at `http://localhost:8080`.

## How to Use

1.  **Register a VM**:
    *   Navigate to the "VM Management" section.
    *   The dropdown will show VMs detected by `VBoxManage`.
    *   Select a VM, and provide the SSH user, the **local path** to the corresponding private SSH key, and the SSH port.
    *   Click "Register VM".

2.  **Deploy an Application**:
    *   Go to the "Deploy Application" section.
    *   Select the target VM.
    *   Choose the `.zip` file containing your application. Your application binary should be pre-compiled for the VM's architecture (e.g., `linux/amd64`).
    *   Specify the destination folder on the VM (e.g., `/opt/myapp`).
    *   Provide the name of the main executable binary inside the zip.
    *   Add any command-line arguments your application needs.
    *   Define a name for the systemd service (e.g., `myapp.service`).
    *   Click "Deploy and Create Service".

3.  **Control a Service**:
    *   Open the "Service Dashboard".
    *   Select the VM and enter the service name you just created.
    *   The dashboard will connect via WebSocket and show the live status.
    *   Use the buttons to start, stop, restart, or manage the service's autostart behavior.

4.  **Monitor Logs**:
    *   Go to the "Live Monitor" section.
    *   Select the VM and provide the full path to a log file (e.g., `/opt/myapp/execution_log.txt`).
    *   Click "Connect" to start streaming the log file in real-time.
