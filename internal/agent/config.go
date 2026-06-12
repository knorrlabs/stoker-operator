package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the agent runtime configuration loaded from env vars and mounted
// files. Git credentials are not held here: the git client reads the
// GIT_SSH_KEY_FILE / GIT_TOKEN_FILE / GIT_KNOWN_HOSTS_FILE env vars directly on
// every operation so rotated Secrets are picked up without a restart.
type Config struct {
	PodName      string
	PodNamespace string
	GatewayName  string
	CRName       string
	CRNamespace  string
	RepoPath     string
	DataPath     string
	GatewayPort  string
	GatewayTLS   bool
	APIKeyFile   string
	SyncPeriod   int    // seconds
	ProfileName  string // profile name from embedded spec.sync.profiles
}

// LoadConfig reads agent configuration from environment variables.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		PodName:      os.Getenv("POD_NAME"),
		PodNamespace: os.Getenv("POD_NAMESPACE"),
		GatewayName:  os.Getenv("GATEWAY_NAME"),
		CRName:       os.Getenv("CR_NAME"),
		CRNamespace:  os.Getenv("CR_NAMESPACE"),
		RepoPath:     os.Getenv("REPO_PATH"),
		DataPath:     os.Getenv("DATA_PATH"),
		GatewayPort:  os.Getenv("GATEWAY_PORT"),
		APIKeyFile:   os.Getenv("API_KEY_FILE"),
		ProfileName:  os.Getenv("PROFILE"),
	}

	// Defaults
	if cfg.RepoPath == "" {
		cfg.RepoPath = "/repo"
	}
	if cfg.DataPath == "" {
		cfg.DataPath = "/ignition-data"
	}
	if cfg.GatewayPort == "" {
		cfg.GatewayPort = "8088"
	}
	if cfg.CRNamespace == "" {
		cfg.CRNamespace = cfg.PodNamespace
	}
	if cfg.GatewayName == "" {
		cfg.GatewayName = cfg.PodName
	}

	// Parse TLS
	cfg.GatewayTLS, _ = strconv.ParseBool(os.Getenv("GATEWAY_TLS"))

	// Parse sync period (default 30s)
	cfg.SyncPeriod = 30
	if sp := os.Getenv("SYNC_PERIOD"); sp != "" {
		if v, err := strconv.Atoi(sp); err == nil && v > 0 {
			cfg.SyncPeriod = v
		}
	}

	// Validate required fields. ProfileName may be empty: pods without a
	// stoker.io/profile annotation fall back to the "default" profile at
	// lookup time, per the CRD contract.
	if cfg.CRName == "" {
		return nil, fmt.Errorf("CR_NAME env var is required")
	}
	if cfg.PodNamespace == "" {
		return nil, fmt.Errorf("POD_NAMESPACE env var is required")
	}

	return cfg, nil
}

// APIKey reads the Ignition API key from the mounted file.
func (c *Config) APIKey() string {
	if c.APIKeyFile == "" {
		return ""
	}
	data, err := os.ReadFile(c.APIKeyFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// GatewayScheme returns "https" or "http" based on TLS setting.
func (c *Config) GatewayScheme() string {
	if c.GatewayTLS {
		return "https"
	}
	return "http"
}

// GatewayHost returns the gateway address for API calls (localhost:port).
func (c *Config) GatewayHost() string {
	return "localhost:" + c.GatewayPort
}
