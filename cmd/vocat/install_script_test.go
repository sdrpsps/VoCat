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
