package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

const (
	localCenterInstallDir = "/opt/vastora/center"
	localAgentDataDir     = "/var/lib/vastora/agent"
)

func runLocalManagement(arguments []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("local Vastora management currently supports Linux hosts")
	}
	switch arguments[0] {
	case "status":
		if len(arguments) != 1 {
			return errors.New("usage: vastora status")
		}
		return reportLocalStatus(localCenterInstallDir, localAgentDataDir, os.Stdout, &http.Client{Timeout: 3 * time.Second})
	case "update":
		if len(arguments) != 1 {
			return errors.New("usage: vastora update")
		}
		if os.Geteuid() != 0 {
			return errors.New("vastora update must run as root")
		}
		if localCenterInstalled(localCenterInstallDir) {
			return runLocalCenterScript(localCenterInstallDir, "install.sh", "center", "--install-dir", localCenterInstallDir)
		}
		return runAgent([]string{"update", "--data-dir", localAgentDataDir})
	case "uninstall":
		if len(arguments) != 1 {
			return errors.New("usage: vastora uninstall")
		}
		if os.Geteuid() != 0 {
			return errors.New("vastora uninstall must run as root")
		}
		if localCenterInstalled(localCenterInstallDir) {
			return runLocalCenterScript(localCenterInstallDir, "uninstall.sh", "--install-dir", localCenterInstallDir)
		}
		if !localAgentInstalled(localAgentDataDir) {
			return errors.New("no Vastora Center or Agent is installed on this host")
		}
		deleteData, cancelled, err := chooseLocalAgentUninstall(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		if cancelled {
			fmt.Println("Cancelled; nothing was changed.")
			return nil
		}
		arguments := []string{"uninstall", "--purge"}
		if deleteData {
			arguments = append(arguments, "--delete-data")
		}
		return runAgent(arguments)
	default:
		return errors.New("unsupported local management command")
	}
}

func localAgentInstalled(dataDir string) bool {
	if info, err := os.Stat(filepath.Join(dataDir, "agent.db")); err == nil && info.Mode().IsRegular() {
		return true
	}
	raw, err := os.ReadFile(vastoraAgentUnitPath)
	return err == nil && strings.Contains(string(raw), "Description=Vastora Agent")
}

func chooseLocalAgentUninstall(input io.Reader, output io.Writer) (deleteData, cancelled bool, err error) {
	reader := bufio.NewReader(input)
	fmt.Fprintln(output, "\nVastora Agent uninstall")
	fmt.Fprintln(output, "  1) Remove Agent and applications; keep application data")
	fmt.Fprintln(output, "  2) Remove Agent, applications, and application data")
	fmt.Fprintln(output, "  3) Cancel")
	fmt.Fprint(output, "Choose [1-3]: ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, false, err
	}
	switch strings.TrimSpace(line) {
	case "1":
		return false, false, nil
	case "2":
		fmt.Fprint(output, "Type DELETE to permanently remove application data: ")
		confirmation, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return false, false, readErr
		}
		if strings.TrimSpace(confirmation) != "DELETE" {
			return false, true, nil
		}
		return true, false, nil
	case "3":
		return false, true, nil
	default:
		return false, false, errors.New("choose 1, 2, or 3")
	}
}

func localCenterInstalled(installDir string) bool {
	for _, name := range []string{".env", "compose.yaml", "release.env"} {
		if info, err := os.Lstat(filepath.Join(installDir, name)); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func runLocalCenterScript(installDir, name string, arguments ...string) error {
	path := filepath.Join(installDir, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("center management script is unavailable: %s", path)
	}
	command := exec.Command(path, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func reportLocalStatus(installDir, agentDataDir string, writer io.Writer, client *http.Client) error {
	found := false
	healthy := true
	if localCenterInstalled(installDir) {
		found = true
		version, err := localEnvironmentValue(filepath.Join(installDir, "release.env"), "VASTORA_VERSION")
		if err != nil {
			return err
		}
		port, err := localEnvironmentValue(filepath.Join(installDir, ".env"), "VASTORA_CENTER_BOOTSTRAP_PORT")
		if err != nil {
			return err
		}
		if port == "" {
			port = "8080"
		}
		endpoint := "http://127.0.0.1:" + port + "/healthz"
		state := "running"
		if !localHTTPHealthy(client, endpoint) {
			state = "unavailable"
			healthy = false
		}
		if version == "" {
			version = "unknown"
		}
		fmt.Fprintf(writer, "Center: %s\nVersion: %s\nLocal address: http://127.0.0.1:%s\n", state, version, port)
	}
	agentDatabase := filepath.Join(agentDataDir, "agent.db")
	if _, err := os.Stat(agentDatabase); err == nil {
		found = true
		state := "running"
		if !localHTTPHealthy(client, "http://127.0.0.1:8090/healthz") {
			state = "unavailable"
			healthy = false
		}
		connection, inspectErr := agent.InspectConnection(agentDataDir)
		if inspectErr != nil {
			fmt.Fprintln(writer, "Agent: unavailable")
			healthy = false
		} else {
			fmt.Fprintf(writer, "Agent: %s\nNode: %s\nAgent Center: %s\n", state, connection.Name, connection.CenterURL)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		found = true
		healthy = false
		fmt.Fprintln(writer, "Agent: unavailable")
	}
	if !found {
		return errors.New("no Vastora Center or Agent is installed on this host")
	}
	if !healthy {
		return errors.New("one or more local Vastora services are unavailable")
	}
	return nil
}

func localHTTPHealthy(client *http.Client, endpoint string) bool {
	response, err := client.Get(endpoint)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func localEnvironmentValue(path, key string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && name == key {
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}
