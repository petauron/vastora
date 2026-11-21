package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"golang.org/x/sys/unix"
)

// FileHostBackupManager queues work for systemd; it must never stop its own container.
type FileHostBackupManager struct {
	InstallDir string
	mu         sync.Mutex
}

func (manager *FileHostBackupManager) directory() string {
	return filepath.Join(manager.InstallDir, ".host-backup")
}
func (manager *FileHostBackupManager) lockHost() (*os.File, error) {
	if err := os.MkdirAll(manager.directory(), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(manager.directory(), "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("host maintenance is in progress")
	}
	return file, nil
}
func backupJSON(path string, fallback string) (json.RawMessage, error) {
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return json.RawMessage(fallback), nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(value) {
		return nil, errors.New("invalid backup state file")
	}
	return value, nil
}
func (manager *FileHostBackupManager) HostBackupStatus(context.Context) (deployapi.HostBackupStatus, error) {
	dir := manager.directory()
	settings := deployapi.HostBackupSettings{Directory: "/srv/vastora-backups", TimeUTC: "03:00", IntervalDays: 1, KeepLast: 7}
	config, err := backupJSON(filepath.Join(dir, "config.json"), `{}`)
	if err != nil {
		return deployapi.HostBackupStatus{}, err
	}
	if err := json.Unmarshal(config, &settings); err != nil {
		return deployapi.HostBackupStatus{}, err
	}
	execution, err := backupJSON(filepath.Join(dir, "status.json"), `{"state":"idle"}`)
	if err != nil {
		return deployapi.HostBackupStatus{}, err
	}
	coverage, err := backupJSON(filepath.Join(dir, "coverage.json"), `{"containers":[],"units":[],"sources":[]}`)
	if err != nil {
		return deployapi.HostBackupStatus{}, err
	}
	if regularFile(filepath.Join(dir, "request")) {
		execution = json.RawMessage(`{"state":"queued"}`)
	}
	snapshots, err := backupJSON(filepath.Join(dir, "snapshots.json"), `[]`)
	if err != nil {
		return deployapi.HostBackupStatus{}, err
	}
	return deployapi.HostBackupStatus{Available: regularFile(filepath.Join(manager.InstallDir, ".backup-service-enabled")),
		PasswordSet: regularFile(filepath.Join(dir, "password")), Settings: settings, Execution: execution, Coverage: coverage, Snapshots: snapshots}, nil
}
func (manager *FileHostBackupManager) ConfigureHostBackup(ctx context.Context, input deployapi.HostBackupInput) (deployapi.HostBackupStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lock, lockErr := manager.lockHost()
	if lockErr != nil {
		return deployapi.HostBackupStatus{}, lockErr
	}
	defer lock.Close()
	if _, err := time.Parse("15:04", input.TimeUTC); err != nil {
		return deployapi.HostBackupStatus{}, errors.New("backup time must use HH:MM UTC")
	}
	if input.IntervalDays < 1 || input.IntervalDays > 30 || input.KeepLast < 1 || input.KeepLast > 90 {
		return deployapi.HostBackupStatus{}, errors.New("backup interval must be 1–30 days; retention must be 1–90 snapshots")
	}
	if !filepath.IsAbs(input.Directory) || filepath.Clean(input.Directory) != input.Directory || input.Directory == "/" || strings.ContainsAny(input.Directory, "\r\n\x00") {
		return deployapi.HostBackupStatus{}, errors.New("backup directory must be an absolute host path")
	}
	current, err := manager.HostBackupStatus(ctx)
	if err != nil {
		return current, err
	}
	if !current.Available {
		return current, errors.New("host backup service is not installed")
	}
	if regularFile(filepath.Join(manager.directory(), "restart.json")) {
		return current, errors.New("interrupted backup service recovery is pending")
	}
	var execution struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(current.Execution, &execution); err != nil {
		return current, err
	}
	if execution.State == "running" || regularFile(filepath.Join(manager.directory(), "request")) {
		return current, errors.New("a backup is in progress")
	}
	if strings.ContainsAny(input.Password, "\r\n\x00") {
		return current, errors.New("backup password cannot contain line breaks")
	}
	if input.Password != "" && current.PasswordSet {
		return current, errors.New("existing repository password cannot be replaced")
	}
	if !current.PasswordSet && len(input.Password) < 12 {
		return current, errors.New("save a backup password of at least 12 characters")
	}
	dir := manager.directory()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return current, err
	}
	if !current.PasswordSet {
		if err := writeCenterUpdateFile(filepath.Join(dir, "password"), []byte(input.Password), 0o600); err != nil {
			return current, err
		}
	}
	// Native-service scope is owned by the host administrator, never overwritten by UI settings.
	config, err := json.Marshal(input.HostBackupSettings)
	if err != nil {
		return current, err
	}
	if err := writeCenterUpdateFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		return current, err
	}
	return manager.HostBackupStatus(ctx)
}
func (manager *FileHostBackupManager) StartHostBackup(ctx context.Context) (deployapi.HostBackupStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lock, lockErr := manager.lockHost()
	if lockErr != nil {
		return deployapi.HostBackupStatus{}, lockErr
	}
	defer lock.Close()
	current, err := manager.HostBackupStatus(ctx)
	if err != nil {
		return current, err
	}
	if !regularFile(filepath.Join(manager.directory(), "config.json")) {
		return current, errors.New("save backup settings first")
	}
	if !current.Available || !current.PasswordSet {
		return current, errors.New("configure host backups first")
	}
	if regularFile(filepath.Join(manager.directory(), "restart.json")) {
		return current, errors.New("service recovery is pending")
	}
	if err := writeCenterUpdateFile(filepath.Join(manager.directory(), "request"), []byte("backup\n"), 0o600); err != nil {
		return current, err
	}
	return manager.HostBackupStatus(ctx)
}
func (server *Server) hostBackup(writer http.ResponseWriter, request *http.Request) {
	if server.hostBackups == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("host backups unavailable"))
		return
	}
	var result deployapi.HostBackupStatus
	var err error
	switch request.Method {
	case http.MethodPut:
		var input deployapi.HostBackupInput
		if !decodeRequest(writer, request, &input) {
			return
		}
		result, err = server.hostBackups.ConfigureHostBackup(request.Context(), input)
	case http.MethodPost:
		result, err = server.hostBackups.StartHostBackup(request.Context())
	default:
		result, err = server.hostBackups.HostBackupStatus(request.Context())
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
func (server *Server) WithHostBackupManager(manager deployapi.HostBackupManager) *Server {
	server.hostBackups = manager
	return server
}
