// File: internal/ssh/client.go
package ssh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log/slog"
	"os"
	"path"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Client struct {
	conn *ssh.Client
}

var (
	clientCache = make(map[string]*Client)
	cacheMutex  = &sync.Mutex{}
)

func GetClient(user, host, keyPath string, port int) (*Client, error) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	addr := fmt.Sprintf("%s:%d", host, port)
	cacheKey := fmt.Sprintf("%s@%s", user, addr)

	if client, ok := clientCache[cacheKey]; ok {
		// Ping the connection to see if it's still alive
		_, _, err := client.conn.SendRequest("keepalive@openssh.com", true, nil)
		if err == nil {
			return client, nil
		}
		// Connection is dead, remove from cache
		client.conn.Close()
		delete(clientCache, cacheKey)
	}

	key, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %v", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		// TODO: verify host key
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	client := &Client{conn: conn}
	clientCache[cacheKey] = client

	return client, nil
}

// RunCommand ejecuta un comando remoto y retorna stdout, stderr, error
func (c *Client) RunCommand(cmd string) (string, string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmd); err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("failed to run command: %v, stderr: %s", err, stderr.String())
	}

	return stdout.String(), stderr.String(), nil
}

// UploadFile sube un archivo local al remote path indicado via SCP
func (c *Client) UploadFile(localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %v", err)
	}

	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		fmt.Fprintf(w, "C%#o %d %s\n", stat.Mode().Perm(), stat.Size(), path.Base(remotePath))
		io.Copy(w, file)
		fmt.Fprint(w, "\x00")
	}()

	cmd := fmt.Sprintf("scp -t %q", path.Dir(remotePath))
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("failed to run scp: %v", err)
	}

	return nil
}

// StreamCommand abre una sesión de streaming para comandos long-running
func (c *Client) StreamCommand(ctx context.Context, cmd string, out chan<- string) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("unable to setup stdout for command: %v", err)
	}

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case out <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			slog.Error("error reading from stream", "error", err)
		}
		close(out)
	}()

	go func() {
		<-ctx.Done()
		session.Signal(ssh.SIGTERM)
	}()

	err = session.Wait()
	if err != nil {
		if _, ok := err.(*ssh.ExitError); ok {
			// The program has exited with an exit code != 0
			return nil
		}
		return fmt.Errorf("command finished with error: %v", err)
	}

	return nil
}
