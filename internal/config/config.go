// Package config loads ssh-autoproxy's YAML configuration file.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so it can be written as a plain string
// ("10m", "2s") in the YAML config instead of nanoseconds.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// DirectProfile describes a network under which a route's HostPatterns/
// HostSubnets are reachable directly. All non-empty fields must match for
// the profile to apply (AND semantics).
type DirectProfile struct {
	Name    string `yaml:"name"`
	SSID    string `yaml:"ssid,omitempty"`
	Gateway string `yaml:"gateway,omitempty"`
	Subnet  string `yaml:"subnet,omitempty"`
}

// SocksProxyConfig configures a route's dynamic SOCKS5 forward. A route
// with a nil *SocksProxyConfig has SOCKS5 disabled for it (the daemon
// still maintains its ControlMaster connection for SSH ProxyJump reuse,
// just without -D). When present with Port left unset: Active=true picks
// a random free high port on Bind (127.0.0.1 by default); otherwise Port
// defaults to 1080. An explicit Port is always honored as given.
type SocksProxyConfig struct {
	Active bool   `yaml:"active,omitempty"`
	Bind   string `yaml:"bind,omitempty"`
	Port   int    `yaml:"port,omitempty"`
}

// Route is one independently-managed jump host: the destinations it
// covers, the network(s) under which those destinations are reached
// directly instead, and its own optional SOCKS5 listener.
type Route struct {
	Name                 string            `yaml:"name"`
	JumpHost             string            `yaml:"jump_host"`
	HostPatterns         []string          `yaml:"host_patterns"`
	HostSubnets          []string          `yaml:"host_subnets"`
	DirectProfiles       []DirectProfile   `yaml:"direct_profiles"`
	SocksProxy           *SocksProxyConfig `yaml:"socks_proxy,omitempty"`
	KeepTunnelWhenDirect bool              `yaml:"keep_tunnel_when_direct"`
}

type SSHConfig struct {
	ControlPath         string   `yaml:"control_path"`
	ControlPersist      Duration `yaml:"control_persist"`
	ConnectTimeout      Duration `yaml:"connect_timeout"`
	ServerAliveInterval Duration `yaml:"server_alive_interval"`
	ServerAliveCountMax int      `yaml:"server_alive_count_max"`
}

type PACConfig struct {
	Enabled bool   `yaml:"enabled"`
	Bind    string `yaml:"bind"`
	Port    int    `yaml:"port"`
	Path    string `yaml:"path"`
}

type DaemonConfig struct {
	PollInterval Duration `yaml:"poll_interval"`
}

type BackoffConfig struct {
	Base   Duration `yaml:"base"`
	Max    Duration `yaml:"max"`
	Jitter string   `yaml:"jitter"`
}

type NotifyConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Cooldown Duration `yaml:"cooldown"`
}

type Config struct {
	Routes  []Route       `yaml:"routes"`
	SSH     SSHConfig     `yaml:"ssh"`
	PAC     PACConfig     `yaml:"pac"`
	Daemon  DaemonConfig  `yaml:"daemon"`
	Backoff BackoffConfig `yaml:"backoff"`
	Notify  NotifyConfig  `yaml:"notify"`
}

const defaultSocksPort = 1080

// Default returns a Config with every field set to its built-in default.
// A missing config file is not an error: the tool should stay inert (no
// routes) rather than fail to start.
func Default() *Config {
	return &Config{
		Routes: []Route{},
		SSH: SSHConfig{
			ControlPath:         "~/.ssh/control/%r@%h:%p",
			ControlPersist:      Duration{10 * time.Minute},
			ConnectTimeout:      Duration{5 * time.Second},
			ServerAliveInterval: Duration{15 * time.Second},
			ServerAliveCountMax: 3,
		},
		PAC:     PACConfig{Enabled: true, Bind: "127.0.0.1", Port: 8850, Path: "/proxy.pac"},
		Daemon:  DaemonConfig{PollInterval: Duration{30 * time.Second}},
		Backoff: BackoffConfig{Base: Duration{2 * time.Second}, Max: Duration{2 * time.Minute}, Jitter: "equal"},
		Notify:  NotifyConfig{Enabled: true, Cooldown: Duration{20 * time.Minute}},
	}
}

// FilePath returns the default config file location, honoring
// XDG_CONFIG_HOME and falling back to ~/.config.
func FilePath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "ssh-autoproxy", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ssh-autoproxy", "config.yaml"), nil
}

// ResolvePath returns path unchanged if non-empty, otherwise the default
// XDG config location from FilePath(). Callers that need to know exactly
// which file Load(path) will read (e.g. to watch it for changes) should
// use this rather than duplicating the "" -> FilePath() fallback.
func ResolvePath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	return FilePath()
}

// Load reads the config file at path (or the default XDG location if path
// is empty), overlaying it onto Default(). A missing file yields Default()
// unchanged rather than an error.
func Load(path string) (*Config, error) {
	path, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err := cfg.applyRouteDefaults(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// applyRouteDefaults fills in per-route SocksProxy defaults that
// Default() can't pre-seed, since Routes is a user-supplied list rather
// than a fixed set of fields. An explicit Port is always left as given;
// otherwise Active=true picks a random free port on Bind, and a plain
// (non-active) block falls back to the historical default port.
func (c *Config) applyRouteDefaults() error {
	for i := range c.Routes {
		sp := c.Routes[i].SocksProxy
		if sp == nil {
			continue
		}
		if sp.Bind == "" {
			sp.Bind = "127.0.0.1"
		}
		if sp.Port != 0 {
			continue
		}
		if sp.Active {
			port, err := pickFreePort(sp.Bind)
			if err != nil {
				return fmt.Errorf("route %q: picking a random socks_proxy port: %w", c.Routes[i].Name, err)
			}
			sp.Port = port
		} else {
			sp.Port = defaultSocksPort
		}
	}
	return nil
}

// pickFreePort asks the OS for a free TCP port on bind by briefly
// binding to port 0 and reading back the assigned port.
func pickFreePort(bind string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", ln.Addr())
	}
	return addr.Port, nil
}

// Validate checks constraints not covered by zero-value defaults: loopback-
// only binds for every SOCKS5/PAC listener (this tool is a personal,
// single-machine proxy, never an open relay), required/unique route
// fields, and that enabled routes don't collide on the same bind:port.
func (c *Config) Validate() error {
	seenNames := map[string]bool{}
	seenAddrs := map[string]string{}
	for _, r := range c.Routes {
		if r.Name == "" {
			return fmt.Errorf("every route must have a name")
		}
		if seenNames[r.Name] {
			return fmt.Errorf("duplicate route name %q", r.Name)
		}
		seenNames[r.Name] = true

		if r.JumpHost == "" {
			return fmt.Errorf("route %q: jump_host is required", r.Name)
		}

		if r.SocksProxy != nil {
			if err := requireLoopback(fmt.Sprintf("routes[%s].socks_proxy.bind", r.Name), r.SocksProxy.Bind); err != nil {
				return err
			}
			addr := fmt.Sprintf("%s:%d", r.SocksProxy.Bind, r.SocksProxy.Port)
			if owner, ok := seenAddrs[addr]; ok {
				return fmt.Errorf("routes %q and %q both use socks_proxy %s — give each route a distinct port", owner, r.Name, addr)
			}
			seenAddrs[addr] = r.Name
		}
	}

	if c.PAC.Enabled {
		if err := requireLoopback("pac.bind", c.PAC.Bind); err != nil {
			return err
		}
	}
	return nil
}

func requireLoopback(field, host string) error {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must be a loopback address (e.g. 127.0.0.1), got %q", field, host)
	}
	return nil
}
