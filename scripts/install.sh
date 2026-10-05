#!/usr/bin/env bash
#
# vocat install / update script for systemd and OpenWrt/procd deployments.
#
# Usage:
#   bash install.sh [--check-env] [--skip-vowifi-check] [version]       # run directly when already root
#   sudo bash install.sh [--check-env] [--skip-vowifi-check] [version]  # run through sudo as a normal user
#   bash install.sh --check-env                                           # check VoWiFi host prerequisites
#
# Behavior:
#   - Prompts for script language (中文 / English) as soon as it runs.
#   - If the installed version equals the target version, does nothing (unless --force).
#   - Prompts for Pocket ID settings when missing and saves them automatically.
#   - Does not create local accounts or passwords.
#   - Verifies Linux XFRM/IPsec support required by IMS; on OpenWrt it tries
#     the matching opkg packages first.
#   - (Re)writes a systemd or OpenWrt/procd service and restarts it.
#
# Published script: must contain no secrets, IPs, or passwords.

set -euo pipefail

# --- Publisher configuration -------------------------------------------------
# Default GitHub repository in owner/name form. Publishers: set this to your
# own repo, or override per-run with VOCAT_REPO.
REPO="${VOCAT_REPO:-sdrpsps/VoCat}"
# Prefix public GitHub release URLs with this accelerator. An explicitly empty
# VOCAT_GITHUB_PROXY disables acceleration. API requests and authentication
# remain direct to GitHub; credentials are never forwarded to the accelerator.
GITHUB_PROXY="${VOCAT_GITHUB_PROXY-https://ghfast.top}"

INSTALL_DIR="/opt/vocat/bin"
BINARY_PATH="${INSTALL_DIR}/vocat"
LINK_PATH="/usr/local/bin/vocat"
ENV_DIR="/etc/vocat"
ENV_FILE="${ENV_DIR}/env"
UNIT_PATH="/etc/systemd/system/vocat.service"
OPENWRT_INIT_PATH="/etc/init.d/vocat"

# --- Language ----------------------------------------------------------------
LANG_CHOICE=""

msg() {
    # $1 = zh text, $2 = en text
    if [ "$LANG_CHOICE" = "en" ]; then
        printf '%s\n' "$2"
    else
        printf '%s\n' "$1"
    fi
}

prompt_language() {
    if ! ( : </dev/tty ) 2>/dev/null; then
        case "${VOCAT_LANG:-en}" in
            zh|zh-CN|cn) LANG_CHOICE="zh" ;;
            *) LANG_CHOICE="en" ;;
        esac
        return
    fi
    while true; do
        echo "选择语言 / Select language:  1) 中文   2) English" >/dev/tty
        printf '> ' >/dev/tty
        if ! read -r choice </dev/tty; then
            LANG_CHOICE="en"
            return
        fi
        case "$choice" in
            1|"") LANG_CHOICE="zh"; return ;;
            2) LANG_CHOICE="en"; return ;;
        esac
    done
}

die() {
    msg "$1" "$2" >&2
    exit 1
}

# --- Root --------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "请以 root 身份运行此脚本。" "Run this script as root."

prompt_language

# BusyBox/OpenWrt images often omit coreutils' install(1). Provide the small
# subset used by this script so the same installer works on router firmware.
if ! command -v install >/dev/null 2>&1; then
    install() {
        if [ "${1:-}" = "-d" ]; then
            shift
            local mode="0755"
            if [ "${1:-}" = "-m" ]; then
                mode="$2"
                shift 2
            fi
            mkdir -p "$@"
            chmod "$mode" "$@"
            return
        fi
        local mode="0755"
        if [ "${1:-}" = "-m" ]; then
            mode="$2"
            shift 2
        fi
        [ "$#" -eq 2 ] || return 2
        cp "$1" "$2"
        chmod "$mode" "$2"
    }
fi

# --- Parse args --------------------------------------------------------------
FORCE=0
CHECK_ENV=0
CONFIGURE_OIDC=0
SKIP_VOWIFI_CHECK="${VOCAT_SKIP_VOWIFI_CHECK:-0}"
TARGET_VERSION=""
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        --check-env) CHECK_ENV=1 ;;
        --configure-oidc) CONFIGURE_OIDC=1 ;;
        --skip-vowifi-check) SKIP_VOWIFI_CHECK=1 ;;
        -h|--help)
            msg "用法: bash install.sh [--force] [--configure-oidc] [--check-env] [--skip-vowifi-check] [版本]" "Usage: bash install.sh [--force] [--configure-oidc] [--check-env] [--skip-vowifi-check] [version]"
            exit 0
            ;;
        *) TARGET_VERSION="${arg#v}" ;;
    esac
done

# --- Resolve target version --------------------------------------------------
resolve_target_version() {
    if [ -n "$TARGET_VERSION" ]; then
        TARGET_VERSION="${TARGET_VERSION#v}"
        return
    fi
    local api_url="https://api.github.com/repos/${REPO}/releases/latest"
    local auth_hdr=()
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        auth_hdr=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
    fi
    local resp
    resp=$(curl -fsSL "${auth_hdr[@]}" "$api_url") || die "无法获取最新版本信息。检查网络或 REPO 设置。" "Failed to fetch latest release. Check network or REPO."
    # Parse "tag_name": "vX.Y.Z" without jq.
    local tag
    tag=$(printf '%s\n' "$resp" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
    [ -n "$tag" ] || die "无法解析最新版本的 tag_name。" "Could not parse tag_name from the release response."
    TARGET_VERSION="${tag#v}"
}

# --- Host prerequisites ------------------------------------------------------
is_openwrt() {
    [ -f /etc/openwrt_release ] || [ -x /sbin/procd ]
}

xfrm_works() {
    command -v ip >/dev/null 2>&1 && ip xfrm state list >/dev/null 2>&1
}

opkg_has_package() {
    opkg list "$1" 2>/dev/null | grep -q "^$1 -"
}

install_openwrt_vowifi_packages() {
    msg "正在检查 OpenWrt/Kwrt 的 VoWiFi 内核组件..." "Checking OpenWrt/Kwrt VoWiFi kernel components..."
    opkg update >/dev/null 2>&1 || msg \
        "警告：opkg 软件源更新失败，将使用现有索引继续检查。" \
        "Warning: opkg feed update failed; checking the existing index."

    local packages=""
    local package
    for package in \
        ip-full \
        kmod-ipsec kmod-ipsec4 kmod-ipsec6 \
        kmod-crypto-authenc kmod-crypto-cbc kmod-crypto-aes \
        kmod-crypto-hmac kmod-crypto-sha1; do
        if opkg_has_package "$package"; then
            packages="$packages $package"
        fi
    done
    if [ -n "$packages" ]; then
        # Kernel packages must come from this firmware's own feed. opkg checks
        # the kernel ABI and refuses mismatched modules; never bypass that check.
        # shellcheck disable=SC2086
        opkg install $packages >/dev/null 2>&1 || true
    fi
}

install_linux_ip_tool() {
    command -v ip >/dev/null 2>&1 && return 0
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y iproute2
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y iproute
    elif command -v yum >/dev/null 2>&1; then
        yum install -y iproute
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm iproute2
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache iproute2
    fi
}

install_qmi_support() {
    msg "正在检查 QMI 命令行工具..." "Checking QMI command-line utilities..."
    qmi_proxy_available() {
        command -v qmi-proxy >/dev/null 2>&1 || \
            [ -x /usr/libexec/qmi-proxy ] || \
            [ -x /usr/lib/qmi-proxy ] || \
            [ -x /usr/lib/libqmi-glib/qmi-proxy ]
    }
    if command -v qmicli >/dev/null 2>&1 && qmi_proxy_available; then
        return 0
    fi

    if is_openwrt && command -v opkg >/dev/null 2>&1; then
        opkg update >/dev/null 2>&1 || true
        local pkgs=""
        opkg_has_package qmi-utils && pkgs="$pkgs qmi-utils"
        opkg_has_package libqmi && pkgs="$pkgs libqmi"
        if [ -z "$pkgs" ]; then
            pkgs="qmi-utils libqmi"
        fi
        # shellcheck disable=SC2086
        opkg install $pkgs >/dev/null 2>&1 || true
    elif command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y libqmi-utils || true
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y libqmi-utils || true
    elif command -v yum >/dev/null 2>&1; then
        yum install -y libqmi-utils || true
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm libqmi || true
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache qmi-utils || true
    fi

    if command -v qmicli >/dev/null 2>&1 && qmi_proxy_available; then
        msg "QMI 命令行工具已就绪。" "QMI command-line utilities are ready."
        return 0
    fi
    die \
        "无法安装或找到 qmicli/qmi-proxy。请安装系统提供的 libqmi/qmi-utils 软件包后重试。" \
        "Could not install or find qmicli/qmi-proxy. Install your distribution's libqmi/qmi-utils package and retry."
}

install_pcsc_support() {
    msg "正在检查 USB SIM 读卡器的 PC/SC 运行环境..." "Checking the PC/SC environment for USB SIM readers..."
    local installed=0
    if is_openwrt && command -v opkg >/dev/null 2>&1; then
        opkg update >/dev/null 2>&1 || true
        local packages=""
        opkg_has_package pcscd && packages="$packages pcscd"
        opkg_has_package ccid && packages="$packages ccid"
        opkg_has_package libccid && packages="$packages libccid"
        if [ -n "$packages" ]; then
            # shellcheck disable=SC2086
            opkg install $packages >/dev/null 2>&1 && installed=1 || true
        fi
    elif command -v apt-get >/dev/null 2>&1; then
        if apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y pcscd libccid; then
            installed=1
        fi
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y pcsc-lite pcsc-lite-ccid && installed=1 || true
    elif command -v yum >/dev/null 2>&1; then
        yum install -y pcsc-lite pcsc-lite-ccid && installed=1 || true
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm pcsclite ccid && installed=1 || true
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache pcsc-lite ccid && installed=1 || true
    fi

    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now pcscd.socket >/dev/null 2>&1 || \
            systemctl restart pcscd >/dev/null 2>&1 || true
    elif [ -x /etc/init.d/pcscd ]; then
        /etc/init.d/pcscd enable >/dev/null 2>&1 || true
        /etc/init.d/pcscd restart >/dev/null 2>&1 || /etc/init.d/pcscd start >/dev/null 2>&1 || true
    fi
    if command -v pcscd >/dev/null 2>&1 || [ "$installed" -eq 1 ]; then
        msg "USB SIM 读卡器 PC/SC 环境已就绪。" "USB SIM reader PC/SC environment is ready."
    else
        msg \
            "警告：未能自动安装 pcscd/CCID 驱动；系统仍会显示读卡器并给出修复提示。" \
            "Warning: pcscd/CCID could not be installed automatically; VoCat will still show the reader with a remediation hint."
    fi
}

check_vowifi_environment() {
    if [ "$SKIP_VOWIFI_CHECK" = "1" ]; then
        msg \
            "已跳过 VoWiFi 内核环境检查；IMS 通话和短信可能不可用。" \
            "Skipped the VoWiFi kernel check; IMS calls and SMS may not work."
        return
    fi

    if is_openwrt && command -v opkg >/dev/null 2>&1; then
        # Install the crypto algorithms even when NETLINK_XFRM already works;
        # some minimal images provide xfrm_user but omit AES-CBC/authenc.
        install_openwrt_vowifi_packages
    elif ! xfrm_works; then
        install_linux_ip_tool
    fi
    if xfrm_works; then
        msg "VoWiFi XFRM/IPsec 环境安装并验证成功。" "VoWiFi XFRM/IPsec environment installed and verified."
        return
    fi

    if is_openwrt; then
        die \
            "当前 OpenWrt/Kwrt 内核 $(uname -r) 不支持 NETLINK_XFRM，且软件源没有匹配的 kmod-ipsec。请使用包含 kmod-ipsec、kmod-ipsec4、kmod-ipsec6、kmod-crypto-authenc、kmod-crypto-cbc、kmod-crypto-aes 和 kmod-crypto-sha1 的同版本固件；严禁安装其他内核版本的 kmod。仅使用非 VoWiFi 功能时可加 --skip-vowifi-check。" \
            "The OpenWrt/Kwrt kernel $(uname -r) lacks NETLINK_XFRM and its feed has no matching kmod-ipsec. Use a firmware built with matching kmod-ipsec, kmod-ipsec4/6, crypto-authenc, CBC, AES and SHA1 modules. Never force kmods from another kernel. Use --skip-vowifi-check only for non-VoWiFi operation."
    fi
    die \
        "当前 Linux 内核不支持 XFRM/IPsec，VoWiFi IMS 无法工作。请启用 CONFIG_XFRM、CONFIG_XFRM_USER、CONFIG_INET_ESP、CONFIG_INET6_ESP、AES-CBC 和 HMAC-SHA1；若仅使用非 VoWiFi 功能（蜂窝短信/数据等），可重新运行安装脚本并加 --skip-vowifi-check。" \
        "This Linux kernel lacks XFRM/IPsec required by VoWiFi IMS. Enable CONFIG_XFRM, CONFIG_XFRM_USER, CONFIG_INET_ESP, CONFIG_INET6_ESP, AES-CBC and HMAC-SHA1; or re-run with --skip-vowifi-check if you only need non-VoWiFi features (cellular SMS/data)."
}

# --- Skip if already installed at the same version ---------------------------
skip_if_equal() {
    [ -x "$BINARY_PATH" ] || return 0
    [ "$FORCE" -eq 1 ] && return 0
    local installed
    installed=$("$BINARY_PATH" version 2>/dev/null | awk '{print $2}' | sed -E 's/[[:space:]]*\(.*$//') || return 0
    [ -z "$installed" ] && return 0
    if [ "$installed" = "$TARGET_VERSION" ]; then
        install -d -m 0755 "$(dirname "$LINK_PATH")"
        ln -sfn "$BINARY_PATH" "$LINK_PATH"
        msg "已安装版本 $installed，与目标版本相同，跳过更新。" "Installed version $installed equals target; skipping."
        exit 0
    fi
    msg "当前 $installed -> $TARGET_VERSION，开始更新。" "Updating $installed -> $TARGET_VERSION."
}

# --- Detect architecture -----------------------------------------------------
detect_arch() {
    ARCH_FALLBACK=""
    case "$(uname -m)" in
        x86_64) ARCH="amd64" ;;
        i386|i486|i586|i686) ARCH="386" ;;
        aarch64) ARCH="aarch64"; ARCH_FALLBACK="arm64" ;;
        arm64) ARCH="arm64"; ARCH_FALLBACK="aarch64" ;;
        armv7l|armv7*) ARCH="armv7" ;;
        *) die "不支持的架构: $(uname -m)" "Unsupported architecture: $(uname -m)" ;;
    esac
}

# --- Download + verify -------------------------------------------------------
VOCAT_TMP=""

# curl transfer options for the binary download. -f makes curl fail on HTTP
# errors and -L follows the release-asset redirect. On an interactive terminal
# we show a single-line progress bar so a multi-megabyte download gives visible
# feedback; otherwise (piped, cron, systemd) we stay quiet but still surface
# errors via -S.
if [ -t 2 ]; then
    CURL_DL_OPTS=(-fSL --progress-bar)
else
    CURL_DL_OPTS=(-fsSL)
fi

download_release_file() {
    local url="$1" destination="$2"
    if [ -n "$GITHUB_PROXY" ]; then
        if curl "${CURL_DL_OPTS[@]}" --connect-timeout 15 --speed-time 30 --speed-limit 1024 \
            -o "$destination" "${GITHUB_PROXY%/}/${url}"; then
            return 0
        fi
        msg "加速下载失败，尝试 GitHub 直连 ..." "Accelerated download failed; trying GitHub directly ..."
    fi
    curl "${CURL_DL_OPTS[@]}" --connect-timeout 15 --speed-time 30 --speed-limit 1024 \
        -o "$destination" "$url"
}

download_and_verify() {
    VOCAT_TMP=$(mktemp -d)
    trap 'rm -rf "$VOCAT_TMP"' EXIT
    local base="https://github.com/${REPO}/releases/download/v${TARGET_VERSION}"
    local asset="vocat-linux-${ARCH}"
    msg "下载 $asset ..." "Downloading $asset ..."
    if ! download_release_file "${base}/${asset}" "${VOCAT_TMP}/vocat"; then
        [ -n "$ARCH_FALLBACK" ] || die "下载二进制失败。" "Failed to download the binary."
        asset="vocat-linux-${ARCH_FALLBACK}"
        msg "尝试 $asset ..." "Trying $asset ..."
        download_release_file "${base}/${asset}" "${VOCAT_TMP}/vocat" || die "下载二进制失败。" "Failed to download the binary."
    fi
    download_release_file "${base}/SHA256SUMS" "${VOCAT_TMP}/SHA256SUMS" || die "下载 SHA256SUMS 失败。" "Failed to download SHA256SUMS."

    local expected actual
    # Match a line whose filename field equals the asset (with optional binary-mode * prefix).
    expected=$(awk -v a="$asset" '$2 == a || $2 == ("*" a) {print $1; exit}' "${VOCAT_TMP}/SHA256SUMS")
    [ -n "$expected" ] || die "SHA256SUMS 中找不到 $asset 的校验行。" "$asset not found in SHA256SUMS."
    actual=$(sha256sum "${VOCAT_TMP}/vocat" | awk '{print $1}')
    [ "$actual" = "$expected" ] || die "SHA-256 校验失败。" "SHA-256 verification failed."
    chmod 0755 "${VOCAT_TMP}/vocat"
    "${VOCAT_TMP}/vocat" version >/dev/null 2>&1 || die \
        "下载的二进制文件无法在此系统上运行；未更改当前安装的版本。" \
        "The downloaded binary cannot run on this host; the installed version was not changed."
}

# --- Install binary ----------------------------------------------------------
install_binary() {
    install -d -m 0755 "$INSTALL_DIR"
    install -m 0755 "${VOCAT_TMP}/vocat" "${BINARY_PATH}.new"
    if [ -e "$BINARY_PATH" ]; then
        cp -a "$BINARY_PATH" "${BINARY_PATH}.bak"
    fi
    mv -f "${BINARY_PATH}.new" "$BINARY_PATH"
    install -d -m 0755 "$(dirname "$LINK_PATH")"
    ln -sfn "$BINARY_PATH" "$LINK_PATH"
}

# --- Data directory ----------------------------------------------------------
ensure_data_dir() {
    install -d -m 0755 /opt/vocat/data
    chown -R root:root /opt/vocat
}

# --- Pocket ID configuration and database compatibility ----------------------
check_database() {
    local candidate="${1:-$BINARY_PATH}"
    "$candidate" database-check --database /opt/vocat/data/vocat.db || \
        die "待安装版本无法读取或升级数据库，当前程序尚未替换。" \
            "The candidate cannot read or migrate the database; the installed program was not replaced."
}

read_env_value() {
    local key="$1" line value="${!1:-}"
    if [ -f "$ENV_FILE" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
            case "$line" in "$key="*) value="${line#*=}" ;; esac
        done < "$ENV_FILE"
    fi
    # Accept simple quoted values from a hand-written environment file without
    # sourcing it or evaluating shell expressions.
    case "$value" in
        \"*\") value="${value#\"}"; value="${value%\"}" ;;
        \'*\') value="${value#\'}"; value="${value%\'}" ;;
    esac
    printf '%s' "$value"
}

oidc_env_value_supported() {
    # Both systemd and procd read these as literal, single-line assignments.
    # Pocket ID-generated client secrets use characters supported here.
    case "$1" in *[[:space:]]*|*\"*|*\'*|*\\*) return 1 ;; esac
}

oidc_has_terminal() {
    ( : </dev/tty ) 2>/dev/null
}

prompt_oidc_value() {
    local label="$1" current="$2" secret="$3" required="$4" input
    while true; do
        if [ "$secret" -eq 1 ]; then
            msg "$label（输入隐藏；回车保留，输入 - 清除）:" \
                "$label (hidden; Enter keeps the current value, - clears it):" >/dev/tty
            if ! IFS= read -rs input </dev/tty; then
                printf '\n' >/dev/tty
                die "配置输入已取消，未保存修改。" "Configuration cancelled; changes were not saved."
            fi
            printf '\n' >/dev/tty
        else
            printf '%s [%s]: ' "$label" "$current" >/dev/tty
            IFS= read -r input </dev/tty || die \
                "配置输入已取消，未保存修改。" "Configuration cancelled; changes were not saved."
        fi
        [ -n "$input" ] || input="$current"
        if [ "$secret" -eq 1 ] && [ "$input" = "-" ]; then input=""; fi
        if [ "$required" -eq 1 ] && [ -z "$input" ]; then
            msg "此项不能为空。" "This value is required." >/dev/tty
        elif ! oidc_env_value_supported "$input"; then
            msg "输入不能包含空白、引号或反斜杠，请重新输入。" \
                "Whitespace, quotes and backslashes are not supported; enter the value again." >/dev/tty
        else
            OIDC_INPUT="$input"
            return
        fi
    done
}

configure_oidc() {
    OIDC_WRITE=0
    OIDC_ISSUER=$(read_env_value VOCAT_OIDC_ISSUER)
    OIDC_CLIENT_ID=$(read_env_value VOCAT_OIDC_CLIENT_ID)
    OIDC_REDIRECT_URL=$(read_env_value VOCAT_OIDC_REDIRECT_URL)
    OIDC_CLIENT_SECRET=$(read_env_value VOCAT_OIDC_CLIENT_SECRET)
    if [ "$CONFIGURE_OIDC" -eq 0 ] && [ -n "$OIDC_ISSUER" ] && \
        [ -n "$OIDC_CLIENT_ID" ] && [ -n "$OIDC_REDIRECT_URL" ]; then
        # Environment variables supplied for unattended installation also need
        # to be persisted for the service, which does not inherit this shell.
        if [ ! -f "$ENV_FILE" ] || ! grep -q '^VOCAT_OIDC_ISSUER=.' "$ENV_FILE" || \
            ! grep -q '^VOCAT_OIDC_CLIENT_ID=.' "$ENV_FILE" || \
            ! grep -q '^VOCAT_OIDC_REDIRECT_URL=.' "$ENV_FILE"; then
            OIDC_WRITE=1
        fi
    else
        oidc_has_terminal || die \
            "缺少 Pocket ID 配置且无交互终端。请下载脚本后在终端运行，或通过环境变量提供 OIDC 配置。" \
            "Pocket ID settings are missing and no terminal is available. Download and run the script in a terminal, or supply the OIDC environment variables."
        msg "配置 Pocket ID；Client Secret 仅 confidential client 必填，public client 可留空。" \
            "Configure Pocket ID; a Client Secret is required for confidential clients, and may be empty for public clients."
        prompt_oidc_value "Pocket ID URL / Issuer" "$OIDC_ISSUER" 0 1
        OIDC_ISSUER="$OIDC_INPUT"
        prompt_oidc_value "Client ID" "$OIDC_CLIENT_ID" 0 1
        OIDC_CLIENT_ID="$OIDC_INPUT"
        prompt_oidc_value "Callback URL" "$OIDC_REDIRECT_URL" 0 1
        OIDC_REDIRECT_URL="$OIDC_INPUT"
        prompt_oidc_value "Client Secret" "$OIDC_CLIENT_SECRET" 1 0
        OIDC_CLIENT_SECRET="$OIDC_INPUT"
        unset OIDC_INPUT
        OIDC_WRITE=1
    fi
    if [ "$OIDC_WRITE" -eq 1 ]; then
        for value in "$OIDC_ISSUER" "$OIDC_CLIENT_ID" "$OIDC_REDIRECT_URL" "$OIDC_CLIENT_SECRET"; do
            oidc_env_value_supported "$value" || die \
                "OIDC 配置含不支持的空白、引号或反斜杠。" \
                "OIDC configuration contains unsupported whitespace, quotes or backslashes."
        done
    fi
}

setup_env() {
    install -d -m 0755 "$ENV_DIR"
    local temporary
    temporary=$(mktemp "${ENV_DIR}/.env.XXXXXX")
    local pattern='^VOCAT_ADMIN_(USERNAME|PASSWORD|PASSWORD_B64)='
    if [ "$OIDC_WRITE" -eq 1 ]; then
        pattern="$pattern|^VOCAT_OIDC_(ISSUER|CLIENT_ID|CLIENT_SECRET|REDIRECT_URL)="
    fi
    if [ -f "$ENV_FILE" ]; then
        local status=0
        grep -Ev "$pattern" "$ENV_FILE" > "$temporary" || status=$?
        if [ "$status" -gt 1 ]; then
            rm -f "$temporary"
            die "无法读取原有配置，未覆盖文件。" "Cannot read existing configuration; the file was not replaced."
        fi
    fi
    if [ "$OIDC_WRITE" -eq 1 ]; then
        printf '%s\n' "VOCAT_OIDC_ISSUER=$OIDC_ISSUER" "VOCAT_OIDC_CLIENT_ID=$OIDC_CLIENT_ID" \
            "VOCAT_OIDC_REDIRECT_URL=$OIDC_REDIRECT_URL" "VOCAT_OIDC_CLIENT_SECRET=$OIDC_CLIENT_SECRET" >> "$temporary"
    fi
    # mktemp creates mode 0600 before any secret is written, and rename retains it.
    mv -f "$temporary" "$ENV_FILE"
    chmod 0600 "$ENV_FILE"
    unset OIDC_CLIENT_SECRET
}

# --- systemd unit ------------------------------------------------------------
write_unit() {
    cat > "$UNIT_PATH" <<EOF
[Unit]
Description=vocat cellular and VoWiFi control service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=/opt/vocat
EnvironmentFile=${ENV_FILE}
Environment=VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db
ExecStart=${BINARY_PATH}
Restart=on-failure
RestartSec=3s
TimeoutStartSec=30s
# HTTP, VoWiFi, and modem cleanup have bounded shutdown contexts totalling up
# to 30 seconds. Leave a small margin before systemd resorts to SIGKILL.
TimeoutStopSec=40s
RuntimeDirectory=vocat
RuntimeDirectoryMode=0755

AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=false
ProtectSystem=strict
ProtectHome=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectControlGroups=true
# The web/CLI self-updater verifies a release in this directory and atomically
# renames it over the running binary. Keep the rest of the host read-only.
ReadWritePaths=/opt/vocat/data /opt/vocat/bin
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK AF_PACKET
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true
UMask=0077
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$UNIT_PATH"
}

write_openwrt_init() {
    cat > "$OPENWRT_INIT_PATH" <<'EOF'
#!/bin/sh /etc/rc.common
START=95
STOP=10
USE_PROCD=1
PROCD_TERM_TIMEOUT=40
PROGRAM=/opt/vocat/bin/vocat
ENV_FILE=/etc/vocat/env
MODEM_RECOVERY_SCRIPT=/usr/bin/modem_network_recover_detection.sh
MODEM_LOCK_DIR=/var/run/modem
MODEM_LOCK_FILE=$MODEM_LOCK_DIR/auto_dial_lock
MODEM_LOCK_OWNER=/var/run/vocat.modem_control_lock
acquire_modem_control() {
    [ -x "$MODEM_RECOVERY_SCRIPT" ] || return 0
    mkdir -p "$MODEM_LOCK_DIR" || return 1
    if [ ! -e "$MODEM_LOCK_FILE" ]; then
        if ! printf '%s\n' lock > "$MODEM_LOCK_FILE"; then
            rm -f "$MODEM_LOCK_FILE"
            return 1
        fi
        if ! : > "$MODEM_LOCK_OWNER"; then
            rm -f "$MODEM_LOCK_FILE"
            return 1
        fi
    fi
}
release_modem_control() {
    [ -e "$MODEM_LOCK_OWNER" ] || return 0
    rm -f "$MODEM_LOCK_FILE" "$MODEM_LOCK_OWNER"
}
start_service() {
    if ! acquire_modem_control; then
        logger -t vocat "cannot acquire GL.iNet modem recovery lock"
        # rc.common's rc_procd ignores start_service's return value and would
        # otherwise submit an empty service definition. Exit before its
        # unconditional procd_close_service call and preserve the failure status.
        exit 1
    fi
    procd_open_instance
    procd_set_param command "$PROGRAM" serve
    procd_set_param env VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db
    if [ -r "$ENV_FILE" ]; then
        while IFS='=' read -r name value; do
            case "$name" in VOCAT_*) procd_append_param env "$name=$value" ;; esac
        done < "$ENV_FILE"
    fi
    procd_set_param respawn 3600 5 5
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
service_stopped() { release_modem_control; }
service_triggers() { procd_add_reload_trigger vocat; }
EOF
    chmod 0755 "$OPENWRT_INIT_PATH"
}

write_service() {
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        write_unit
        return
    fi
    if [ -x /sbin/procd ] || [ -x /sbin/ubusd ]; then
        write_openwrt_init
        return
    fi
    die "不支持的服务管理器。" "Neither systemd nor OpenWrt procd was detected."
}

enable_and_start() {
    if [ -x "$OPENWRT_INIT_PATH" ] && { [ -x /sbin/procd ] || [ -x /sbin/ubusd ]; }; then
        "$OPENWRT_INIT_PATH" enable
		# Stop explicitly before restart. Some procd/rc.common variants return
		# from restart while the previous process is still inside its bounded
		# VoWiFi cleanup, so the replacement can race the host-wide instance
		# lock and enter a respawn cycle.
		"$OPENWRT_INIT_PATH" stop || true
		local stop_attempt
		for stop_attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40; do
			if ! "$OPENWRT_INIT_PATH" running; then
				break
			fi
			sleep 1
		done
        if "$OPENWRT_INIT_PATH" restart; then
            # Modems may need several seconds to release and reopen their AT
            # port after procd stops the previous process. Require consecutive
            # healthy observations so a short-lived respawn is not mistaken for
            # a successful upgrade.
            local attempt stable
            stable=0
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
                sleep 1
                if "$OPENWRT_INIT_PATH" running; then
                    stable=$((stable + 1))
                    if [ "$stable" -ge 3 ]; then
                        rm -f "${BINARY_PATH}.bak"
                        return
                    fi
                else
                    stable=0
                fi
            done
        fi
        if [ -e "${BINARY_PATH}.bak" ]; then
            cp -a "${BINARY_PATH}.bak" "$BINARY_PATH"
            "$OPENWRT_INIT_PATH" restart || true
        fi
        die "OpenWrt vocat 服务启动失败。" "The OpenWrt vocat service failed to start."
    fi
    systemctl daemon-reload
    systemctl enable vocat
    if systemctl restart vocat; then
        local attempt
        for attempt in 1 2 3 4 5; do
            if systemctl is-active --quiet vocat; then
                rm -f "${BINARY_PATH}.bak"
                return
            fi
            sleep 1
        done
    fi
    if [ -e "${BINARY_PATH}.bak" ]; then
        msg "新版本启动失败，正在恢复旧二进制。" "The new version failed to start; restoring the previous binary."
        cp -a "${BINARY_PATH}.bak" "$BINARY_PATH"
        systemctl restart vocat || true
    fi
    die "vocat 服务启动失败。" "The vocat service failed to start."
}

# --- Main --------------------------------------------------------------------
detect_arch
install_qmi_support
install_pcsc_support
check_vowifi_environment
if [ "$CHECK_ENV" -eq 1 ]; then
    msg "VoCat 运行环境检查完成。" "VoCat host environment check completed."
    exit 0
fi
resolve_target_version
if [ "$CONFIGURE_OIDC" -eq 0 ]; then skip_if_equal; fi
configure_oidc
download_and_verify
ensure_data_dir
# Validate the database with the downloaded binary before replacing the
# installed program. In particular, a release with an older schema must never
# overwrite a newer working binary and leave the service in a restart loop.
check_database "${VOCAT_TMP}/vocat"
install_binary
setup_env
write_service
enable_and_start

echo
msg "================ 安装/更新完成 ================" "================ Install/update complete ================"
msg "已安装 $TARGET_VERSION，服务已重启。请使用 Pocket ID 登录。" "Installed $TARGET_VERSION; service restarted. Sign in with Pocket ID."
