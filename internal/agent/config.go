package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

// Config is what sys0 remembers between runs. It lives in the data
// directory as config.json, readable only by its owner.
type Config struct {
	NodeAddr    string         `json:"nodeAddr"`
	Fingerprint string         `json:"fingerprint"`
	AgentID     string         `json:"agentId"`
	Secret      string         `json:"secret"`
	Name        string         `json:"name"`
	Trust       protocol.Trust `json:"trust"`
	MaxTasks    int            `json:"maxTasks"`
	// StorageGB is the disk space this machine lends to pooled storage.
	// sys0 also always leaves 5 GB of the disk free.
	StorageGB int `json:"storageGB"`
}

// DefaultStorageGB is lent when the owner has not chosen a size.
const DefaultStorageGB = 10

var ErrNotConfigured = errors.New("sys0 is not set up yet: run `sys0 setup` (or `sys0 join <code>`)")

func configPath(dataDir string) string { return filepath.Join(dataDir, "config.json") }

func LoadConfig(dataDir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotConfigured
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.AgentID == "" || c.Secret == "" {
		return c, ErrNotConfigured
	}
	if c.MaxTasks <= 0 {
		c.MaxTasks = 4
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) == nil {
		if _, set := raw["storageGB"]; !set {
			c.StorageGB = DefaultStorageGB // configs from before pooled storage
		}
	}
	return c, nil
}

// SaveConfig writes atomically so a crash never leaves a half-written file.
func SaveConfig(dataDir string, c Config) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := configPath(dataDir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, configPath(dataDir))
}
