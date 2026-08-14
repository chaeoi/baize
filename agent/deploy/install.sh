#!/bin/sh
set -eu

repo=${BAIZE_REPO:-chaeoi/baize}
version=${BAIZE_VERSION:-latest}
install_dir=/opt/baize/agent
dashboard_url=
token=
robot_code=
robot_uuid=
robot_model=2m_v0.1.2

usage() {
	cat >&2 <<'EOF'
Usage: install.sh --dashboard-url URL --token TOKEN --robot-code CODE [--uuid UUID] [--robot-model MODEL] [--version TAG]
EOF
}

die() {
	echo "baize-agent installer: $*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--dashboard-url)
			[ "$#" -ge 2 ] || die "--dashboard-url requires a value"
			dashboard_url=$2
			shift 2
			;;
		--token)
			[ "$#" -ge 2 ] || die "--token requires a value"
			token=$2
			shift 2
			;;
		--robot-code)
			[ "$#" -ge 2 ] || die "--robot-code requires a value"
			robot_code=$2
			shift 2
			;;
		--uuid)
			[ "$#" -ge 2 ] || die "--uuid requires a value"
			robot_uuid=$2
			shift 2
			;;
		--robot-model)
			[ "$#" -ge 2 ] || die "--robot-model requires a value"
			robot_model=$2
			shift 2
			;;
		--version)
			[ "$#" -ge 2 ] || die "--version requires a value"
			version=$2
			shift 2
			;;
		-h|--help)
			usage >&1
			exit 0
			;;
		*)
			usage
			die "unknown option: $1"
			;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run this installer as root (for example: curl ... | sudo bash -s -- ...)"
[ -n "$dashboard_url" ] || die "--dashboard-url is required"
[ -n "$token" ] || die "--token is required"
[ -n "$robot_code" ] || die "--robot-code is required"
[ "${#token}" -ge 12 ] || die "--token must contain at least 12 characters"

case "$dashboard_url" in
	http://*|https://*) ;;
	*) die "--dashboard-url must start with http:// or https://" ;;
esac

validate_printable() {
	case "$2" in
		*[![:print:]]*) die "$1 must not contain control characters" ;;
	esac
}
validate_printable dashboard_url "$dashboard_url"
validate_printable token "$token"
validate_printable robot_code "$robot_code"
validate_printable robot_model "$robot_model"

if ! printf '%s\n' "$robot_code" | grep -Eq '^[A-Za-z0-9._-]{1,64}$'; then
	die "--robot-code may contain only letters, numbers, dot, underscore and dash"
fi
if ! printf '%s\n' "$robot_model" | grep -Eq '^[A-Za-z0-9._-]+$'; then
	die "--robot-model contains unsupported characters"
fi

if [ -z "$robot_uuid" ]; then
	if [ -r /proc/sys/kernel/random/uuid ]; then
		robot_uuid=$(cat /proc/sys/kernel/random/uuid)
	elif command -v uuidgen >/dev/null 2>&1; then
		robot_uuid=$(uuidgen)
	else
		die "cannot generate a UUID; pass --uuid explicitly"
	fi
fi
if ! printf '%s\n' "$robot_uuid" | grep -Eq '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'; then
	die "--uuid must be a canonical UUID"
fi

id ubuntu >/dev/null 2>&1 || die "the ubuntu user is required by baize-agent.service"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v install >/dev/null 2>&1 || die "install is required"
command -v systemctl >/dev/null 2>&1 || die "systemctl is required"
[ ! -e "$install_dir/config.yml" ] || die "$install_dir/config.yml already exists; edit it directly instead of reinstalling"

case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die "unsupported Linux architecture: $(uname -m)" ;;
esac

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
asset="baize-agent-linux-$arch"
if [ "$version" = latest ]; then
	url="https://github.com/$repo/releases/latest/download/$asset"
else
	url="https://github.com/$repo/releases/download/$version/$asset"
fi
curl --fail --location --silent --show-error "$url" -o "$tmp_dir/$asset"

yaml_quote() {
	escaped=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
	printf '"%s"' "$escaped"
}

dashboard_url_yaml=$(yaml_quote "$dashboard_url")
token_yaml=$(yaml_quote "$token")
robot_code_yaml=$(yaml_quote "$robot_code")
robot_model_yaml=$(yaml_quote "$robot_model")
robot_uuid_yaml=$(yaml_quote "$robot_uuid")
umask 077
cat > "$tmp_dir/config.yml" <<EOF
agent:
  uuid: $robot_uuid_yaml
  robot_code: $robot_code_yaml
  robot_model: $robot_model_yaml
  dashboard_url: $dashboard_url_yaml
  token: $token_yaml
EOF

cat > "$tmp_dir/baize-agent.service" <<'EOF'
[Unit]
Description=Baize robot monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
Group=ubuntu
WorkingDirectory=/opt/baize/agent
ExecStart=/opt/baize/agent/baize-agent -config /opt/baize/agent/config.yml
Restart=always
RestartSec=3

AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true

ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/opt/baize/agent
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF

"$tmp_dir/$asset" -config "$tmp_dir/config.yml" -check-config
install -d -o ubuntu -g ubuntu -m 0750 "$install_dir"
install -o ubuntu -g ubuntu -m 0755 "$tmp_dir/$asset" "$install_dir/baize-agent"
install -o ubuntu -g ubuntu -m 0600 "$tmp_dir/config.yml" "$install_dir/config.yml"
install -o root -g root -m 0644 "$tmp_dir/baize-agent.service" /etc/systemd/system/baize-agent.service
systemctl daemon-reload
systemctl enable --now baize-agent
echo "baize-agent installed for $robot_code ($robot_uuid)"
