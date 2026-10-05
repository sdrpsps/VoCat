package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual shell download/verification functions without installing
// services or contacting a network. curl returns deterministic release assets.
func TestInstallerAcceleratedReleaseDownloads(t *testing.T) {
	script, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), "# --- Download + verify")
	end := strings.Index(string(script), "# --- Install binary")
	if start < 0 || end <= start {
		t.Fatal("installer download functions not found")
	}
	for _, test := range []struct {
		name, mode, proxy, arch, fallback string
		wantError                         bool
	}{
		{name: "accelerated", mode: "ok", proxy: "https://ghfast.top", arch: "amd64"},
		{name: "direct fallback", mode: "fallback", proxy: "https://ghfast.top/", arch: "amd64"},
		{name: "acceleration disabled", mode: "ok", arch: "amd64"},
		{name: "ARM asset alias", mode: "arm", proxy: "https://ghfast.top", arch: "aarch64", fallback: "arm64"},
		{name: "checksum mismatch", mode: "tamper", proxy: "https://ghfast.top", arch: "amd64", wantError: true},
		{name: "download failure", mode: "failure", proxy: "https://ghfast.top", arch: "amd64", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			body := `set -euo pipefail
msg() { :; }
die() { printf '%s\n' "$2" >&2; exit 1; }
GITHUB_PROXY="$TEST_PROXY"
REPO=sdrpsps/VoCat
TARGET_VERSION=1.2.3
ARCH="$TEST_ARCH"
ARCH_FALLBACK="$TEST_ARCH_FALLBACK"
curl() {
    local url="${!#}" destination=""
    printf '%s\n' "$url" >> "$TEST_DIR/requests"
    while [ "$#" -gt 0 ]; do
        if [ "$1" = -o ]; then destination="$2"; shift; fi
        shift
    done
    if [ "$TEST_MODE" = failure ] ||
       { [ "$TEST_MODE" = fallback ] && [[ "$url" == https://ghfast.top/* ]]; } ||
       { [ "$TEST_MODE" = arm ] && [[ "$url" == */vocat-linux-aarch64 ]]; }; then
        printf 'partial failed download' > "$destination"
        return 22
    fi
    if [[ "$url" == */SHA256SUMS ]]; then
        local hash
        hash=$(sha256sum "$VOCAT_TMP/vocat" | awk '{print $1}')
        if [ "$TEST_MODE" = tamper ]; then hash=incorrect; fi
        printf '%s  %s\n' "$hash" "$downloaded_asset" > "$destination"
    else
        downloaded_asset="${url##*/}"
        printf '#!/bin/sh\nexit 0\n' > "$destination"
    fi
}
` + string(script[start:end]) + "\ndownload_and_verify\n"
			command := exec.Command("bash", "-c", body)
			command.Env = append(os.Environ(), "TEST_DIR="+dir, "TEST_PROXY="+test.proxy,
				"TEST_MODE="+test.mode, "TEST_ARCH="+test.arch, "TEST_ARCH_FALLBACK="+test.fallback)
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantError {
				t.Fatalf("download result = %v, output=%s", err, output)
			}
			requests, err := os.ReadFile(filepath.Join(dir, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(requests)), "\n")
			mirror := "https://ghfast.top/https://github.com/sdrpsps/VoCat/releases/download/v1.2.3/"
			direct := "https://github.com/sdrpsps/VoCat/releases/download/v1.2.3/"
			switch test.name {
			case "accelerated":
				if len(lines) != 2 || lines[0] != mirror+"vocat-linux-amd64" || lines[1] != mirror+"SHA256SUMS" {
					t.Fatalf("accelerated requests = %s", requests)
				}
			case "direct fallback":
				if len(lines) != 4 || lines[0] != mirror+"vocat-linux-amd64" || lines[1] != direct+"vocat-linux-amd64" || lines[2] != mirror+"SHA256SUMS" || lines[3] != direct+"SHA256SUMS" {
					t.Fatalf("fallback requests = %s", requests)
				}
			case "acceleration disabled":
				if len(lines) != 2 || strings.Contains(string(requests), "ghfast.top") {
					t.Fatalf("direct requests = %s", requests)
				}
			case "ARM asset alias":
				if len(lines) != 4 || lines[2] != mirror+"vocat-linux-arm64" || lines[3] != mirror+"SHA256SUMS" {
					t.Fatalf("ARM requests = %s", requests)
				}
			case "checksum mismatch":
				if !strings.Contains(string(output), "SHA-256 verification failed") {
					t.Fatalf("tampered file not rejected by checksum: %s", output)
				}
			}
		})
	}
}

func TestInstallerValidatesDatabaseBeforeReplacingBinary(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	mainStart := strings.LastIndex(script, "# --- Main ")
	if mainStart < 0 {
		t.Fatal("installer main section not found")
	}
	main := script[mainStart:]
	validateAt := strings.Index(main, `check_database "${VOCAT_TMP}/vocat"`)
	installAt := strings.Index(main, "install_binary")
	if validateAt < 0 {
		t.Fatal("installer does not validate the database with the downloaded binary")
	}
	if installAt < 0 {
		t.Fatal("installer does not install the downloaded binary")
	}
	if validateAt > installAt {
		t.Fatal("installer replaces the current binary before validating database compatibility")
	}
}

func TestInstallerProvidesRequiredQMIUtilities(t *testing.T) {
	scriptBytes, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"install_qmi_support()",
		"command -v qmicli",
		"command -v qmi-proxy",
		"/usr/libexec/qmi-proxy",
		"/usr/lib/qmi-proxy",
		"apt-get install -y libqmi-utils",
		"dnf install -y libqmi-utils",
		"pacman -Sy --noconfirm libqmi",
		"apk add --no-cache qmi-utils",
		"Could not install or find qmicli/qmi-proxy",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer is missing required QMI handling %q", required)
		}
	}
	mainStart := strings.LastIndex(script, "# --- Main ")
	if mainStart < 0 || !strings.Contains(script[mainStart:], "install_qmi_support") {
		t.Error("installer does not install QMI utilities from its main path")
	}
}

func TestInstallerPocketIDConfiguration(t *testing.T) {
	script, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), "read_env_value()")
	end := strings.Index(string(script), "# --- systemd unit")
	if start < 0 || end <= start {
		t.Fatal("installer configuration functions not found")
	}
	complete := "VOCAT_ADDR=127.0.0.1:7575\nVOCAT_OIDC_ISSUER=https://auth.example.com\nVOCAT_OIDC_CLIENT_ID=client-id\nVOCAT_OIDC_REDIRECT_URL=https://vocat.example.com/api/auth/oidc/callback\nVOCAT_OIDC_CLIENT_SECRET=saved-test-secret\n"
	for _, test := range []struct {
		name, existing, mode, secret                 string
		terminal, reconfigure, unattended, wantError bool
	}{
		{name: "first interactive install", existing: "VOCAT_ADDR=127.0.0.1:7575\nVOCAT_ADMIN_PASSWORD=obsolete\n", terminal: true, secret: "new-test-secret"},
		{name: "existing configuration reused without terminal", existing: complete, secret: "saved-test-secret"},
		{name: "keep secret while reconfiguring", existing: complete, terminal: true, reconfigure: true, mode: "keep", secret: "saved-test-secret"},
		{name: "clear secret for public client", existing: complete, terminal: true, reconfigure: true, mode: "clear"},
		{name: "unattended installation", unattended: true, secret: "injected-test-secret"},
		{name: "missing configuration without terminal", existing: "VOCAT_ADDR=127.0.0.1:7575\n", wantError: true},
		{name: "cancelled input", existing: "VOCAT_ADDR=127.0.0.1:7575\n", terminal: true, mode: "cancel", wantError: true},
		{name: "reject multiline injected secret", unattended: true, secret: "first\nVOCAT_ADDR=unwanted", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "env")
			if test.existing != "" {
				if err := os.WriteFile(file, []byte(test.existing), 0644); err != nil {
					t.Fatal(err)
				}
			}
			body := `set -euo pipefail
umask 000
ENV_DIR="$TEST_DIR"
ENV_FILE="$ENV_DIR/env"
CONFIGURE_OIDC="$TEST_RECONFIGURE"
msg() { printf '%s\n' "$2"; }
die() { msg "$1" "$2" >&2; exit 1; }
` + string(script[start:end]) + `
oidc_has_terminal() { [ "$TEST_TERMINAL" = 1 ]; }
prompt_oidc_value() {
    if [ "$TEST_MODE" = cancel ]; then die "cancelled" "cancelled"; fi
    if [ "$TEST_MODE" = keep ] || [ "$TEST_MODE" = clear ]; then
        OIDC_INPUT="$2"
        if [ "$3" = 1 ] && [ "$TEST_MODE" = clear ]; then OIDC_INPUT=""; fi
        return
    fi
    case "$1" in
        'Pocket ID URL / Issuer') OIDC_INPUT=https://auth.example.com ;;
        'Client ID') OIDC_INPUT=client-id ;;
        'Callback URL') OIDC_INPUT=https://vocat.example.com/api/auth/oidc/callback ;;
        'Client Secret') OIDC_INPUT="$TEST_SECRET" ;;
    esac
}
configure_oidc
setup_env
`
			command := exec.Command("bash", "-c", body)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "VOCAT_OIDC_") {
					command.Env = append(command.Env, entry)
				}
			}
			flag := func(v bool) string {
				if v {
					return "1"
				}
				return "0"
			}
			command.Env = append(command.Env, "TEST_DIR="+dir, "TEST_TERMINAL="+flag(test.terminal), "TEST_RECONFIGURE="+flag(test.reconfigure), "TEST_MODE="+test.mode, "TEST_SECRET="+test.secret)
			if test.unattended {
				command.Env = append(command.Env, "VOCAT_OIDC_ISSUER=https://auth.example.com", "VOCAT_OIDC_CLIENT_ID=client-id", "VOCAT_OIDC_REDIRECT_URL=https://vocat.example.com/api/auth/oidc/callback", "VOCAT_OIDC_CLIENT_SECRET="+test.secret)
			}
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantError {
				t.Fatalf("configuration error=%v output=%s", err, output)
			}
			if test.secret != "" && strings.Contains(string(output), test.secret) {
				t.Fatal("secret leaked to output")
			}
			data, readErr := os.ReadFile(file)
			if test.wantError {
				if test.existing == "" && !os.IsNotExist(readErr) {
					t.Fatal("failed configuration created a file")
				}
				if test.existing != "" && (readErr != nil || string(data) != test.existing) {
					t.Fatal("failed configuration modified existing settings")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(data), "VOCAT_OIDC_CLIENT_SECRET="+test.secret+"\n") {
				t.Fatal("secret was not preserved or replaced correctly")
			}
			if !strings.Contains(string(data), "VOCAT_OIDC_CLIENT_ID=client-id\n") {
				t.Fatal("missing client ID")
			}
			if test.existing != "" && !strings.Contains(string(data), "VOCAT_ADDR=127.0.0.1:7575\n") {
				t.Fatal("other configuration was lost")
			}
			if strings.Contains(string(data), "VOCAT_ADMIN_PASSWORD=") {
				t.Fatal("legacy password was retained")
			}
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("configuration permissions must be 0600")
			}
		})
	}
}
