# ServiciosLinuxGo

Plataforma web desarrollada en Go para automatizar despliegue, control y monitoreo de aplicaciones en maquinas virtuales Linux de VirtualBox mediante systemd.

## Proposito del proyecto

El objetivo es centralizar en una sola interfaz web tareas que normalmente se hacen por consola:

1. Registrar una VM con sus credenciales SSH.
2. Subir un paquete ZIP de aplicacion.
3. Crear automaticamente un servicio systemd.
4. Controlar el daemon (start/stop/restart/enable/disable).
5. Visualizar estado y logs en tiempo real.

## Funcionalidades implementadas

1. Gestion de VMs registradas
- Descubrimiento de VMs con `VBoxManage`.
- Registro persistente en `config/vms.json`.
- Carga y guardado de llave privada SSH en `config/keys/`.

2. Despliegue automatizado desde ZIP
- Recepcion de archivo `.zip` por formulario web.
- Validacion basica de firma ZIP (`PK`).
- Extraccion temporal y carga de archivos a la VM via SSH/SCP.
- Generacion de `run.sh` con log incremental en `execution_log.txt`.

3. Integracion con systemd
- Generacion de archivo `.service` dinamico.
- Publicacion en `/etc/systemd/system/<servicio>.service`.
- Ejecucion de `daemon-reload` tras el despliegue.
- Control remoto del servicio: iniciar, detener, reiniciar, habilitar y deshabilitar.

4. Dashboard y monitoreo en tiempo real
- Estado del servicio via WebSocket de estado (polling cada 3 segundos).
- Streaming de logs con `tail -f` via WebSocket.
- Vista Live Monitor para observar el archivo de log remoto.

## Stack y herramientas usadas

1. Backend
- Go (`net/http`, `log/slog`, `archive/zip`, `os/exec`).
- `golang.org/x/crypto/ssh` para ejecucion remota y transferencia.
- `github.com/gorilla/websocket` para canales en tiempo real.

2. Frontend
- `index.html` + JavaScript y CSS en `web/static/`.
- Fetch API para endpoints REST.
- WebSocket nativo del navegador para estado/logs.

3. Infraestructura
- VirtualBox (`VBoxManage`) para descubrimiento e informacion de VMs.
- Linux + systemd en la VM destino.

## Estructura actual del proyecto

```
/main.go                        # Servidor HTTP, rutas API y handlers
/index.html                     # Interfaz principal
/web/static/
  app.js                        # Logica del frontend
  style.css                     # Estilos de la interfaz
/internal/
  vbox/manager.go               # Integracion con VBoxManage
  ssh/client.go                 # Cliente SSH reutilizable + SCP
  deploy/deployer.go            # Flujo de despliegue ZIP -> VM
  systemd/service.go            # Generacion y control de servicios
  ws/hub.go                     # Endpoints WebSocket (status y tail)
/config/
  vms.json                      # Persistencia de VMs registradas
  keys/                         # Llaves SSH cargadas por el usuario
```

## Flujo funcional de extremo a extremo

1. Registro de VM
- El usuario selecciona la VM, usuario SSH, puerto y llave privada.
- La plataforma guarda la configuracion para reutilizarla en deploy/control.

2. Deploy de aplicacion
- Se sube un ZIP con la aplicacion.
- Se extrae temporalmente, se crea `run.sh` y se sube todo a la ruta destino.
- Se genera y publica el servicio systemd con `ExecStart` apuntando a `run.sh`.

3. Activacion y control del daemon
- Desde Service Dashboard se ejecutan acciones de control remoto (`start`, `stop`, `restart`, `enable`, `disable`).
- El estado se actualiza automaticamente en la UI.

4. Observabilidad
- Live Monitor se conecta al archivo remoto de log (ejemplo: `/opt/hola_mundo/execution_log.txt`).
- El contenido se transmite en vivo al navegador.

## Requisitos previos

1. Go 1.21 o superior.
2. VirtualBox instalado y `VBoxManage` disponible en PATH.
3. VM Linux con:
- SSH activo.
- Usuario con acceso por llave privada.
- `sudo` para operaciones de systemd.

## Configuracion de sudoers en la VM

Para operar servicios sin pedir contrasena en cada comando, configurar `sudoers` con `visudo`:

```sh
your_ssh_user ALL=(ALL) NOPASSWD: /bin/systemctl start *, /bin/systemctl stop *, /bin/systemctl restart *, /bin/systemctl enable *, /bin/systemctl disable *, /bin/systemctl daemon-reload, /usr/bin/tee /etc/systemd/system/*
```

Usar solo en entornos controlados de laboratorio/desarrollo.

## Compilacion y ejecucion

En la raiz del proyecto:

```sh
go build -o plataforma.exe .
```

Ejecucion:

```sh
./plataforma.exe
```

La interfaz queda disponible en:

```text
http://localhost:8080
```

## Endpoints principales

1. REST
- `GET /api/vms` - listar VMs registradas.
- `POST /api/vms` - registrar VM.
- `GET /api/vms/discover` - descubrir VMs de VirtualBox.
- `GET /api/vms/{name}/info` - obtener estado/IP de VM.
- `POST /api/deploy` - desplegar ZIP y crear servicio.
- `POST /api/service/start|stop|restart|enable|disable` - control del servicio.
- `GET /api/service/status` - estado del servicio.

2. WebSocket
- `/ws/status` - estado continuo del servicio.
- `/ws/tail` - streaming de `tail -f` de un archivo remoto.

## Notas operativas

1. El deploy crea el servicio y hace `daemon-reload`, pero el inicio del daemon se ejecuta desde el dashboard (boton Start).
2. Si la aplicacion no inicia, verificar primero en la VM:

```sh
sudo systemctl status <servicio>.service --no-pager -l
sudo journalctl -u <servicio>.service --since "5 minutes ago" --no-pager -l
```
