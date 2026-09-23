package main

import (
	"strings"
	"testing"
)

func TestRenderUnit(t *testing.T) {
	tests := []struct {
		name, authSock, runtimeDir, want string
	}{
		{"under runtime dir", "/run/user/1000/ssh-agent.socket", "/run/user/1000", "Environment=SSH_AUTH_SOCK=%t/ssh-agent.socket\n"},
		{"runtime dir trailing slash", "/run/user/1000/gcr/ssh", "/run/user/1000/", "Environment=SSH_AUTH_SOCK=%t/gcr/ssh\n"},
		{"elsewhere", "/tmp/ssh-XXXX/agent.123", "/run/user/1000", "Environment=SSH_AUTH_SOCK=/tmp/ssh-XXXX/agent.123\n"},
		{"no runtime dir", "/run/user/1000/ssh-agent.socket", "", "Environment=SSH_AUTH_SOCK=/run/user/1000/ssh-agent.socket\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderUnit("/usr/bin/ssh-autoproxy", tt.authSock, tt.runtimeDir)
			if !strings.Contains(got, tt.want) {
				t.Errorf("unit missing %q:\n%s", tt.want, got)
			}
			if !strings.Contains(got, "ExecStart=/usr/bin/ssh-autoproxy daemon\n") {
				t.Errorf("unit missing ExecStart:\n%s", got)
			}
		})
	}

	t.Run("unset", func(t *testing.T) {
		got := renderUnit("/usr/bin/ssh-autoproxy", "", "/run/user/1000")
		if strings.Contains(got, "SSH_AUTH_SOCK") {
			t.Errorf("unexpected SSH_AUTH_SOCK line:\n%s", got)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("format error in unit:\n%s", got)
		}
	})
}
