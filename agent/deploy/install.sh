#!/bin/sh
set -eu

repo=${BAIZE_REPO:-chaeoi/baize}
version=${BAIZE_VERSION:-latest}
mirror=${BAIZE_MIRROR:-https://gitwarp.canghai.org}
dashboard_url=${BAIZE_DASHBOARD_URL:-}
token=${BAIZE_AGENT_TOKEN:-}
robot_code=${BAIZE_ROBOT_CODE:-}
robot_model=${BAIZE_ROBOT_MODEL:-}
robot_uuid=${BAIZE_AGENT_UUID:-}
force_config=false
config_path=${BAIZE_CONFIG_PATH:-/opt/baize/agent/config.yml}

tty=/dev/tty

prompt() {
	label=$1
	default=${2:-}
	value=
	if [ -r "$tty" ] && [ -w "$tty" ]; then
		if [ -n "$default" ]; then
			printf '%s [%s]: ' "$label" "$default" >"$tty"
		else
			printf '%s: ' "$label" >"$tty"
		fi
		IFS= read -r value <"$tty" || true
	fi
	[ -n "$value" ] || value=$default
	printf '%s' "$value"
}

usage() {
	cat <<'EOF'
Usage: install.sh [service install options] [--version TAG]

Service options:
  --dashboard-url URL --token TOKEN --robot-code CODE --robot-model MODEL
  [--uuid UUID]
  [--force-config]

With missing values, terminal sessions prompt for them. Non-interactive
sessions must provide the four required BAIZE_* variables or matching flags.

For a one-line install, set BAIZE_DASHBOARD_URL, BAIZE_AGENT_TOKEN,
BAIZE_ROBOT_CODE, and BAIZE_ROBOT_MODEL. When run from a terminal, missing
values are requested interactively. BAIZE_AGENT_UUID is optional.
EOF
}

die() {
	echo "baize-agent installer: $*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--dashboard-url|--token|--robot-code|--robot-model|--uuid|--version)
			[ "$#" -ge 2 ] || die "$1 requires a value"
			case "$1" in
				--dashboard-url) dashboard_url=$2 ;;
				--token) token=$2 ;;
				--robot-code) robot_code=$2 ;;
				--robot-model) robot_model=$2 ;;
				--uuid) robot_uuid=$2 ;;
				--version) version=$2 ;;
			esac
			shift 2
			;;
		--force-config)
			force_config=true
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown option: $1"
			;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run this installer as root"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die "unsupported Linux architecture: $(uname -m)" ;;
esac

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
asset="baize-agent-linux-$arch"
if [ "$version" = latest ]; then
	path="releases/latest/download"
else
	path="releases/download/$version"
fi
download_base() {
	base=$1
	curl --fail --location --retry 4 --retry-delay 2 --retry-all-errors --silent --show-error "$base/$repo/$path/$2" \
		-o "$tmp_dir/$2"
}
download_release() {
	base=$1
	download_base "$base" "$asset"
	download_base "$base" SHA256SUMS
	(cd "$tmp_dir" && grep "  $asset\$" SHA256SUMS | sha256sum -c -)
}
if ! download_release "$mirror/github.com"; then
	download_release "https://github.com" || die "release download or checksum verification failed"
fi
chmod 0755 "$tmp_dir/$asset"

# A valid existing config can be reused for upgrades and repeated installs;
# the downloaded binary is the authority for the current config schema.
existing_config=false
if [ -r "$config_path" ] && "$tmp_dir/$asset" --config "$config_path" --check-config >/dev/null 2>&1; then
	existing_config=true
fi
if [ "$existing_config" = false ]; then
	if [ -z "$robot_uuid" ] && [ -r "$config_path" ]; then
		robot_uuid=$(awk '$1 == "uuid:" {print $2; exit}' "$config_path" | tr -d '"' | tr -d "'")
	fi
	if [ -z "$dashboard_url" ] && [ -r "$tty" ] && [ -w "$tty" ]; then
		dashboard_url=$(prompt "Dashboard URL" "https://baize.example.com")
	fi
	if [ -z "$token" ] && [ -r "$tty" ] && [ -w "$tty" ]; then
		token=$(prompt "Dashboard Agent token")
	fi
	if [ -z "$robot_code" ] && [ -r "$tty" ] && [ -w "$tty" ]; then
		robot_code=$(prompt "Robot code" "M99")
	fi
	if [ -z "$robot_model" ] && [ -r "$tty" ] && [ -w "$tty" ]; then
		robot_model=$(prompt "Robot model" "2m_v0.1.2")
	fi
	[ -n "$dashboard_url" ] || die "Dashboard URL is required (set BAIZE_DASHBOARD_URL or pass --dashboard-url)"
	[ -n "$token" ] || die "Agent token is required (set BAIZE_AGENT_TOKEN or pass --token)"
	[ -n "$robot_code" ] || die "Robot code is required (set BAIZE_ROBOT_CODE or pass --robot-code)"
	[ -n "$robot_model" ] || die "Robot model is required (set BAIZE_ROBOT_MODEL or pass --robot-model)"
fi

# The binary owns config creation, validation, permissions, and systemd setup.
# Rebuild positional arguments so values remain safely quoted after extracting
# the download-only --version option.
set --
[ -z "$dashboard_url" ] || set -- "$@" --dashboard-url "$dashboard_url"
[ -z "$token" ] || set -- "$@" --token "$token"
[ -z "$robot_code" ] || set -- "$@" --robot-code "$robot_code"
[ -z "$robot_model" ] || set -- "$@" --robot-model "$robot_model"
[ -z "$robot_uuid" ] || set -- "$@" --uuid "$robot_uuid"
[ "$force_config" = false ] || set -- "$@" --force-config
"$tmp_dir/$asset" service install "$@"
