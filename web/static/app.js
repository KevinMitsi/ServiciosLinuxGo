// File: web/static/app.js
document.addEventListener('DOMContentLoaded', () => {
    function appLog(message, data) {
        if (data !== undefined) {
            console.log(`[VM Manager] ${message}`, data);
            return;
        }
        console.log(`[VM Manager] ${message}`);
    }

    async function apiFetch(url, options) {
        appLog('API request', { url, options: options || {} });
        const response = await fetch(url, options);
        appLog('API response', { url, status: response.status, ok: response.ok });
        return response;
    }

    async function readErrorMessage(response) {
        try {
            const data = await response.json();
            if (data && data.error) {
                return data.error;
            }
        } catch (_e) {
            // Ignore and fallback to text.
        }

        try {
            const text = await response.text();
            if (text) {
                return text;
            }
        } catch (_e) {
            // Ignore and fallback below.
        }

        return `Request failed with status ${response.status}`;
    }

    const sections = {
        vms: document.getElementById('vms-section'),
        deploy: document.getElementById('deploy-section'),
        dashboard: document.getElementById('dashboard-section'),
        monitor: document.getElementById('monitor-section'),
    };

    const navLinks = {
        vms: document.querySelector('nav a[href="#vms"]'),
        deploy: document.querySelector('nav a[href="#deploy"]'),
        dashboard: document.querySelector('nav a[href="#dashboard"]'),
        monitor: document.querySelector('nav a[href="#monitor"]'),
    };

    function showSection(sectionId) {
        appLog('Show section', sectionId);
        Object.values(sections).forEach(section => section.style.display = 'none');
        sections[sectionId].style.display = 'block';
        Object.values(navLinks).forEach(link => link.classList.remove('active'));
        navLinks[sectionId].classList.add('active');
    }

    Object.keys(navLinks).forEach(key => {
        navLinks[key].addEventListener('click', (e) => {
            e.preventDefault();
            showSection(key);
        });
    });

    // Default section
    showSection('vms');

    // VM Management
    const vmList = document.getElementById('vm-list');
    const newVmForm = document.getElementById('new-vm-form');
    const vmNameSelect = document.getElementById('vm-name-select');
    const sshKeyFileInput = document.getElementById('ssh-key-file');
    const sshKeyPathInput = document.getElementById('ssh-key-path');

    sshKeyFileInput.addEventListener('change', () => {
        if (!sshKeyFileInput.files || sshKeyFileInput.files.length === 0) {
            sshKeyPathInput.value = '';
            return;
        }

        const selectedFile = sshKeyFileInput.files[0];
        appLog('SSH key file selected', { name: selectedFile.name, size: selectedFile.size });
        sshKeyPathInput.value = `Pending save: ${selectedFile.name}`;
    });

    async function loadVms() {
        const response = await apiFetch('/api/vms');
        const vms = await response.json();
        appLog('Registered VMs loaded', vms);
        vmList.innerHTML = '';
        vms.forEach(vm => {
            const row = document.createElement('tr');
            row.innerHTML = `
                <td>${vm.name}</td>
                <td>${vm.ssh_user}</td>
                <td>${vm.ssh_port}</td>
                <td id="vm-status-${vm.name}">loading...</td>
                <td><button class="get-ip-btn" data-vm="${vm.name}">Get IP</button></td>
            `;
            vmList.appendChild(row);
            updateVmStatus(vm.name);
        });
    }

    async function populateVmSelect() {
        const response = await apiFetch('/api/vms/discover');
        const vms = await response.json();
        appLog('VBox VMs discovered for combobox', vms);
        vmNameSelect.innerHTML = '<option value="">Select a VM</option>';
        vms.forEach(vm => {
            const option = document.createElement('option');
            option.value = vm.name;
            option.textContent = vm.name;
            vmNameSelect.appendChild(option);
        });
    }

    async function updateVmStatus(vmName) {
        const statusCell = document.getElementById(`vm-status-${vmName}`);
        try {
            const response = await apiFetch(`/api/vms/${vmName}/info`);
            const info = await response.json();
            appLog('VM status updated', { vmName, info });
            statusCell.textContent = `${info.state} (${info.ip})`;
            if (info.state === 'running') {
                statusCell.style.color = 'green';
            } else {
                statusCell.style.color = 'red';
            }
        } catch (error) {
            appLog('VM status update failed', { vmName, error });
            statusCell.textContent = 'Error';
            statusCell.style.color = 'orange';
        }
    }

    newVmForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const formData = new FormData();
        formData.set('name', vmNameSelect.value);
        formData.set('ssh_user', document.getElementById('ssh-user').value);
        formData.set('ssh_port', document.getElementById('ssh-port').value);

        if (!sshKeyFileInput.files || sshKeyFileInput.files.length === 0) {
            alert('Please select an SSH private key file.');
            return;
        }

        formData.set('ssh_key_file', sshKeyFileInput.files[0]);
        appLog('Register VM submit', {
            name: vmNameSelect.value,
            ssh_user: document.getElementById('ssh-user').value,
            ssh_port: document.getElementById('ssh-port').value,
            ssh_key_file: sshKeyFileInput.files[0].name,
        });

        const response = await apiFetch('/api/vms', {
            method: 'POST',
            body: formData,
        });

        if (!response.ok) {
            throw new Error(await readErrorMessage(response));
        }

        const result = await response.json();
        appLog('VM registered', result);
        sshKeyPathInput.value = result.ssh_key_path || '';
        newVmForm.reset();
        if (result.ssh_key_path) {
            sshKeyPathInput.value = result.ssh_key_path;
        }
        loadVms();
    });

    vmList.addEventListener('click', async (e) => {
        if (e.target.classList.contains('get-ip-btn')) {
            const vmName = e.target.dataset.vm;
            updateVmStatus(vmName);
        }
    });

    // Deploy
    const deployForm = document.getElementById('deploy-form');
    const deployVmSelect = document.getElementById('deploy-vm-select');
    const progressBar = document.getElementById('progress-bar');
    const progressText = document.getElementById('progress-text');

    async function loadDeployVms() {
        const response = await apiFetch('/api/vms');
        const vms = await response.json();
        appLog('Deploy VM list loaded', vms);
        deployVmSelect.innerHTML = '<option value="">Select a VM</option>';
        vms.forEach(vm => {
            const option = document.createElement('option');
            option.value = vm.name;
            option.textContent = vm.name;
            deployVmSelect.appendChild(option);
        });
    }

    deployForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const formData = new FormData(deployForm);
        const selectedFile = formData.get('zip_file');

        if (!(selectedFile instanceof File) || !selectedFile.name.toLowerCase().endsWith('.zip')) {
            progressText.textContent = 'Error: solo se admite archivo .zip (no .rar).';
            progressBar.style.backgroundColor = 'red';
            progressBar.parentElement.style.display = 'block';
            appLog('Deploy blocked: invalid file format', selectedFile ? selectedFile.name : 'none');
            return;
        }
        
        progressBar.style.width = '0%';
        progressText.textContent = 'Starting...';
        progressBar.parentElement.style.display = 'block';

        try {
            const response = await apiFetch('/api/deploy', {
                method: 'POST',
                body: formData,
            });

            if (!response.ok) {
                throw new Error(await readErrorMessage(response));
            }
            
            // This is a simplified progress indicator.
            // A real implementation would use websockets or polling.
            let progress = 0;
            const interval = setInterval(() => {
                progress += 25;
                progressBar.style.width = progress + '%';
                if (progress === 25) progressText.textContent = 'Uploading...';
                if (progress === 50) progressText.textContent = 'Extracting...';
                if (progress === 75) progressText.textContent = 'Creating service...';
                if (progress >= 100) {
                    clearInterval(interval);
                    progressText.textContent = 'Done!';
                }
            }, 500);

        } catch (error) {
            progressText.textContent = `Error: ${error.message}`;
            progressBar.style.backgroundColor = 'red';
        }
    });

    // Dashboard
    const dashboardVmSelect = document.getElementById('dashboard-vm-select');
    const serviceNameInput = document.getElementById('dashboard-service-name');
    const statusBadge = document.getElementById('status-badge');
    const subStatusBadge = document.getElementById('sub-status-badge');
    const statusOutput = document.getElementById('status-output');
    const controlButtons = {
        start: document.getElementById('start-btn'),
        stop: document.getElementById('stop-btn'),
        restart: document.getElementById('restart-btn'),
        enable: document.getElementById('enable-btn'),
        disable: document.getElementById('disable-btn'),
    };

    let statusSocket;

    async function loadDashboardVms() {
        const response = await apiFetch('/api/vms');
        const vms = await response.json();
        appLog('Dashboard VM list loaded', vms);
        dashboardVmSelect.innerHTML = '<option value="">Select a VM</option>';
        vms.forEach(vm => {
            const option = document.createElement('option');
            option.value = vm.name;
            option.textContent = vm.name;
            dashboardVmSelect.appendChild(option);
        });
    }

    function connectStatusWs() {
        const vmName = dashboardVmSelect.value;
        const serviceName = serviceNameInput.value;
        if (!vmName || !serviceName) return;

        if (statusSocket) {
            statusSocket.close();
        }

        const wsProtocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
        const wsUrl = `${wsProtocol}://${window.location.host}/ws/status?vm=${encodeURIComponent(vmName)}&service=${encodeURIComponent(serviceName)}`;
        appLog('Connecting status websocket', wsUrl);
        statusSocket = new WebSocket(wsUrl);

        statusSocket.onmessage = (event) => {
            try {
                const payload = JSON.parse(event.data);
                if (payload.error) {
                    statusOutput.textContent = payload.error;
                    statusBadge.textContent = 'error';
                    statusBadge.className = 'badge failed';
                    appLog('Status websocket payload error', payload.error);
                    return;
                }

                updateDashboard(payload);
            } catch (_error) {
                statusOutput.textContent = `Invalid status payload: ${event.data}`;
                appLog('Invalid status websocket payload', event.data);
            }
        };
        statusSocket.onerror = (err) => {
            console.error('Status WS Error:', err);
            statusOutput.textContent = 'WebSocket connection error.';
        };
        statusSocket.onclose = () => {
            appLog('Status websocket closed');
        };
    }

    function updateDashboard(status) {
        statusBadge.textContent = status.active_state;
        subStatusBadge.textContent = status.sub_state;
        statusOutput.textContent = status.raw_output;

        if (status.active_state === 'active') {
            statusBadge.className = 'badge active';
            controlButtons.start.disabled = true;
            controlButtons.stop.disabled = false;
            controlButtons.restart.disabled = false;
        } else if (status.active_state === 'inactive' || status.active_state === 'failed') {
            statusBadge.className = status.active_state === 'failed' ? 'badge failed' : 'badge inactive';
            controlButtons.start.disabled = false;
            controlButtons.stop.disabled = true;
            controlButtons.restart.disabled = true;
        } else {
            statusBadge.className = 'badge';
        }
        
        // This is a simplification. A real check for enabled/disabled is needed.
        // controlButtons.enable.disabled = ?
        // controlButtons.disable.disabled = ?
    }
    
    dashboardVmSelect.addEventListener('change', connectStatusWs);
    serviceNameInput.addEventListener('change', connectStatusWs);

    Object.keys(controlButtons).forEach(action => {
        controlButtons[action].addEventListener('click', async () => {
            const vmName = dashboardVmSelect.value;
            const serviceName = serviceNameInput.value;
            await apiFetch(`/api/service/${action}`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ vm_name: vmName, service_name: serviceName }),
            });
            // The websocket will update the status automatically
        });
    });


    // Monitor
    const monitorVmSelect = document.getElementById('monitor-vm-select');
    const logFileInput = document.getElementById('log-file-path');
    const connectBtn = document.getElementById('connect-log-btn');
    const logOutput = document.getElementById('log-output');
    const lineCount = document.getElementById('line-count');
    let logSocket;
    let lines = 0;

    async function loadMonitorVms() {
        const response = await apiFetch('/api/vms');
        const vms = await response.json();
        appLog('Monitor VM list loaded', vms);
        monitorVmSelect.innerHTML = '<option value="">Select a VM</option>';
        vms.forEach(vm => {
            const option = document.createElement('option');
            option.value = vm.name;
            option.textContent = vm.name;
            monitorVmSelect.appendChild(option);
        });
    }

    connectBtn.addEventListener('click', () => {
        if (logSocket && logSocket.readyState === WebSocket.OPEN) {
            logSocket.close();
            connectBtn.textContent = 'Connect';
            return;
        }

        const vmName = monitorVmSelect.value;
        const filePath = logFileInput.value;
        if (!vmName || !filePath) {
            alert('Please select a VM and enter a log file path.');
            return;
        }

        logOutput.textContent = '';
        lines = 0;
        lineCount.textContent = '0';

        const wsUrl = `ws://${window.location.host}/ws/tail?vm=${vmName}&file=${filePath}`;
        appLog('Connecting log websocket', wsUrl);
        logSocket = new WebSocket(wsUrl);

        logSocket.onopen = () => {
            connectBtn.textContent = 'Disconnect';
        };

        logSocket.onmessage = (event) => {
            logOutput.textContent += event.data + '\n';
            logOutput.scrollTop = logOutput.scrollHeight;
            lines++;
            lineCount.textContent = lines;
        };

        logSocket.onclose = () => {
            connectBtn.textContent = 'Connect';
        };

        logSocket.onerror = (error) => {
            console.error('Log WS Error:', error);
            logOutput.textContent += 'WebSocket connection error.\n';
        };
    });


    // Initial loads
    loadVms();
    populateVmSelect();
    loadDeployVms();
    loadDashboardVms();
    loadMonitorVms();
});
