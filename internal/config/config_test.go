package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Routes) != 0 {
		t.Errorf("expected an inert config with no routes, got %+v", cfg.Routes)
	}
	if cfg.PAC.Port != Default().PAC.Port {
		t.Errorf("PAC.Port = %d, want default %d", cfg.PAC.Port, Default().PAC.Port)
	}
}

func TestLoadPartialPACOverridesKeepOtherDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "routes: []\npac:\n  port: 9999\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PAC.Port != 9999 {
		t.Errorf("PAC.Port = %d, want 9999", cfg.PAC.Port)
	}
	def := Default()
	if cfg.PAC.Bind != def.PAC.Bind {
		t.Errorf("PAC.Bind = %q, want default %q (unset fields must keep their default)", cfg.PAC.Bind, def.PAC.Bind)
	}
	if cfg.PAC.Path != def.PAC.Path {
		t.Errorf("PAC.Path = %q, want default %q", cfg.PAC.Path, def.PAC.Path)
	}
	if cfg.PAC.Enabled != def.PAC.Enabled {
		t.Errorf("PAC.Enabled = %v, want default %v", cfg.PAC.Enabled, def.PAC.Enabled)
	}
}

func TestLoadNoPACSectionAtAllUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("routes: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	def := Default()
	if cfg.PAC != def.PAC {
		t.Errorf("PAC = %+v, want default %+v", cfg.PAC, def.PAC)
	}
}

const exampleConfig = `
routes:
  - name: home-lan
    jump_host: gateway.example-tailnet.ts.net
    host_patterns:
      - "*.lan"
    direct_profiles:
      - name: home-wifi
        ssid: HomeWiFi
        gateway: 10.20.0.1
        subnet: 10.20.0.0/21
    socks_proxy:
      bind: 127.0.0.1
      port: 1080

  - name: work
    jump_host: bastion.work.example.com
    host_patterns:
      - "*.corp"
    direct_profiles:
      - name: office-wifi
        ssid: CorpWifi
    socks_proxy:
      bind: 127.0.0.1
      port: 1081

ssh:
  control_path: "~/.ssh/control/%r@%h:%p"
  control_persist: 10m
  connect_timeout: 5s
  server_alive_interval: 15s
  server_alive_count_max: 3

pac:
  enabled: true
  bind: 127.0.0.1
  port: 8850
  path: /proxy.pac

daemon:
  poll_interval: 30s

backoff:
  base: 2s
  max: 2m
  jitter: equal

notify:
  enabled: true
  cooldown: 20m
`

func TestLoadExampleConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(exampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(cfg.Routes))
	}

	home := cfg.Routes[0]
	if home.Name != "home-lan" || home.JumpHost != "gateway.example-tailnet.ts.net" {
		t.Errorf("home route = %+v", home)
	}
	if len(home.DirectProfiles) != 1 || home.DirectProfiles[0].SSID != "HomeWiFi" {
		t.Errorf("home DirectProfiles = %+v", home.DirectProfiles)
	}
	if home.SocksProxy == nil || home.SocksProxy.Port != 1080 {
		t.Errorf("home SocksProxy = %+v", home.SocksProxy)
	}

	work := cfg.Routes[1]
	if work.Name != "work" || work.JumpHost != "bastion.work.example.com" {
		t.Errorf("work route = %+v", work)
	}
	if work.SocksProxy == nil || work.SocksProxy.Port != 1081 {
		t.Errorf("work SocksProxy = %+v", work.SocksProxy)
	}

	if cfg.SSH.ControlPersist.Duration != 10*time.Minute {
		t.Errorf("ControlPersist = %v", cfg.SSH.ControlPersist.Duration)
	}
	if cfg.Notify.Cooldown.Duration != 20*time.Minute {
		t.Errorf("Notify.Cooldown = %v", cfg.Notify.Cooldown.Duration)
	}
}

func TestFilePathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config-test")
	path, err := FilePath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/xdg-config-test", "ssh-autoproxy", "config.yaml")
	if path != want {
		t.Errorf("FilePath() = %q, want %q", path, want)
	}
}

func TestApplyRouteDefaultsPlainBlockUsesDefaultPort(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{}},
	}
	if err := cfg.applyRouteDefaults(); err != nil {
		t.Fatalf("applyRouteDefaults() error = %v", err)
	}
	if cfg.Routes[0].SocksProxy.Bind != "127.0.0.1" {
		t.Errorf("Bind = %q, want 127.0.0.1", cfg.Routes[0].SocksProxy.Bind)
	}
	if cfg.Routes[0].SocksProxy.Port != defaultSocksPort {
		t.Errorf("Port = %d, want %d", cfg.Routes[0].SocksProxy.Port, defaultSocksPort)
	}
}

func TestApplyRouteDefaultsActiveTruePicksRandomPort(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{Active: true}},
		{Name: "b", JumpHost: "b.example.com", SocksProxy: &SocksProxyConfig{Active: true}},
	}
	if err := cfg.applyRouteDefaults(); err != nil {
		t.Fatalf("applyRouteDefaults() error = %v", err)
	}
	for _, r := range cfg.Routes {
		if r.SocksProxy.Bind != "127.0.0.1" {
			t.Errorf("route %s: Bind = %q, want 127.0.0.1", r.Name, r.SocksProxy.Bind)
		}
		if r.SocksProxy.Port == 0 {
			t.Errorf("route %s: expected a non-zero random port", r.Name)
		}
	}
	if cfg.Routes[0].SocksProxy.Port == cfg.Routes[1].SocksProxy.Port {
		t.Errorf("expected distinct random ports, both got %d", cfg.Routes[0].SocksProxy.Port)
	}
}

func TestApplyRouteDefaultsExplicitPortWinsOverActive(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{Active: true, Port: 9999}},
	}
	if err := cfg.applyRouteDefaults(); err != nil {
		t.Fatalf("applyRouteDefaults() error = %v", err)
	}
	if cfg.Routes[0].SocksProxy.Port != 9999 {
		t.Errorf("Port = %d, want 9999 (explicit port must win over active's random assignment)", cfg.Routes[0].SocksProxy.Port)
	}
}

func TestLoadRouteWithNilSocksProxyStaysDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "routes:\n  - name: a\n    jump_host: a.example.com\n    host_patterns: [\"*.a\"]\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Routes[0].SocksProxy != nil {
		t.Errorf("expected SocksProxy to stay nil when socks_proxy is omitted, got %+v", cfg.Routes[0].SocksProxy)
	}
}

func TestLoadRouteWithActiveTrueGetsRandomPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "routes:\n  - name: a\n    jump_host: a.example.com\n    host_patterns: [\"*.a\"]\n    socks_proxy:\n      active: true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	sp := cfg.Routes[0].SocksProxy
	if sp == nil {
		t.Fatal("expected socks_proxy to be enabled")
	}
	if sp.Bind != "127.0.0.1" || sp.Port == 0 {
		t.Errorf("expected a random port on 127.0.0.1, got %+v", sp)
	}
}

func TestValidateRejectsNonLoopbackSocksBind(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{Bind: "0.0.0.0", Port: 1080}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for a non-loopback socks_proxy.bind")
	}
}

func TestValidateRejectsNonLoopbackPACBind(t *testing.T) {
	cfg := Default()
	cfg.PAC.Bind = "192.168.1.5"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for a non-loopback pac.bind")
	}
}

func TestValidateRejectsDuplicateRouteNames(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "dup", JumpHost: "a.example.com"},
		{Name: "dup", JumpHost: "b.example.com"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for duplicate route names")
	}
}

func TestValidateRejectsMissingJumpHost(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{{Name: "a"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for a route with no jump_host")
	}
}

func TestValidateRejectsSocksPortCollision(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{Bind: "127.0.0.1", Port: 1080}},
		{Name: "b", JumpHost: "b.example.com", SocksProxy: &SocksProxyConfig{Bind: "127.0.0.1", Port: 1080}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for two routes sharing the same socks_proxy bind:port")
	}
}

func TestValidateAllowsDistinctSocksPorts(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{
		{Name: "a", JumpHost: "a.example.com", SocksProxy: &SocksProxyConfig{Bind: "127.0.0.1", Port: 1080}},
		{Name: "b", JumpHost: "b.example.com", SocksProxy: &SocksProxyConfig{Bind: "127.0.0.1", Port: 1081}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateAllowsRouteWithNoSocksProxy(t *testing.T) {
	cfg := Default()
	cfg.Routes = []Route{{Name: "a", JumpHost: "a.example.com"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}
