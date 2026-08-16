#!/bin/sh
set -eu

repo=${BAIZE_REPO:-chaeoi/baize}
version=${BAIZE_VERSION:-latest}
dashboard_url=
token=
robot_code=
robot_model=
robot_uuid=
force_config=false

usage() {
	cat <<'EOF'
Usage: install.sh [service install options] [--version TAG]

Service options:
  --dashboard-url URL --token TOKEN --robot-code CODE --robot-model MODEL
  [--uuid UUID]
  [--force-config]

With no service options, the Agent generates a default config for editing.
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
	base_url="https://github.com/$repo/releases/latest/download"
else
	base_url="https://github.com/$repo/releases/download/$version"
fi
curl --fail --location --silent --show-error "$base_url/$asset" -o "$tmp_dir/$asset"
curl --fail --location --silent --show-error "$base_url/SHA256SUMS" -o "$tmp_dir/SHA256SUMS"
(cd "$tmp_dir" && grep "  $asset\$" SHA256SUMS | sha256sum -c -) || die "release checksum verification failed"
chmod 0755 "$tmp_dir/$asset"

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
