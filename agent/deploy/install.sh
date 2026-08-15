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
		--dashboard-url|--token|--robot-code|--uuid|--robot-model|--version)
			[ "$#" -ge 2 ] || die "$1 requires a value"
			case "$1" in
				--dashboard-url) dashboard_url=$2 ;;
				--token) token=$2 ;;
				--robot-code) robot_code=$2 ;;
				--uuid) robot_uuid=$2 ;;
				--robot-model) robot_model=$2 ;;
				--version) version=$2 ;;
			esac
			shift 2
			;;
		-h|--help) usage >&1; exit 0 ;;
		*) usage; die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run this installer as root"
[ -n "$dashboard_url" ] || die "--dashboard-url is required"
[ -n "$token" ] || die "--token is required"
[ -n "$robot_code" ] || die "--robot-code is required"
[ "${#token}" -ge 12 ] || die "--token must contain at least 12 characters"
case "$dashboard_url" in http://*|https://*) ;; *) die "invalid dashboard URL" ;; esac

validate_printable() {
	case "$2" in *[![:print:]]*) die "$1 contains control characters" ;; esac
}
validate_printable dashboard_url "$dashboard_url"
validate_printable token "$token"
validate_printable robot_code "$robot_code"
validate_printable robot_model "$robot_model"
printf '%s\n' "$robot_code" | grep -Eq '^[A-Za-z0-9._-]{1,64}$' || die "invalid robot code"
printf '%s\n' "$robot_model" | grep -Eq '^[A-Za-z0-9._-]{1,64}$' || die "invalid robot model"

if [ -z "$robot_uuid" ]; then
	if [ -r /proc/sys/kernel/random/uuid ]; then robot_uuid=$(cat /proc/sys/kernel/random/uuid)
	elif command -v uuidgen >/dev/null 2>&1; then robot_uuid=$(uuidgen)
	else die "cannot generate a UUID; pass --uuid explicitly"; fi
fi
printf '%s\n' "$robot_uuid" | grep -Eq '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$' || die "invalid UUID"

id ubuntu >/dev/null 2>&1 || die "the ubuntu user is required"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
command -v install >/dev/null 2>&1 || die "install is required"
command -v systemctl >/dev/null 2>&1 || die "systemctl is required"

case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die "unsupported Linux architecture: $(uname -m)" ;;
esac

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
asset="baize-agent-linux-$arch"
if [ "$version" = latest ]; then base_url="https://github.com/$repo/releases/latest/download"
else base_url="https://github.com/$repo/releases/download/$version"; fi
curl --fail --location --silent --show-error "$base_url/$asset" -o "$tmp_dir/$asset"
curl --fail --location --silent --show-error "$base_url/SHA256SUMS" -o "$tmp_dir/SHA256SUMS"
(cd "$tmp_dir" && grep "  $asset\$" SHA256SUMS | sha256sum -c -) || die "release checksum verification failed"

yaml_quote() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}
uuid_yaml=$(yaml_quote "$robot_uuid")
code_yaml=$(yaml_quote "$robot_code")
model_yaml=$(yaml_quote "$robot_model")
url_yaml=$(yaml_quote "$dashboard_url")
token_yaml=$(yaml_quote "$token")
cat > "$tmp_dir/config.yml" <<EOF
agent:
  uuid: "$uuid_yaml"
  robot_code: "$code_yaml"
  robot_model: "$model_yaml"
  dashboard_url: "$url_yaml"
  token: "$token_yaml"
  report_interval: "2s"
  http_timeout: "10s"

system:
  enabled: true
  disk_paths: ["/"]

gpu:
  enabled: true
  command: "nvidia-smi"
  timeout: "3s"

update:
  enabled: true
  automatic: true
  check_interval: "1m"
EOF
"$tmp_dir/$asset" --check-config --config "$tmp_dir/config.yml"

cat > "$tmp_dir/baize-agent.service" <<EOF
[Unit]
Description=Baize robot monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
Group=ubuntu
WorkingDirectory=$install_dir
ExecStart=$install_dir/baize-agent --config $install_dir/config.yml
Restart=always
RestartSec=3
Environment=ROS_LOG_DIR=/var/log/baize-agent/ros
LogsDirectory=baize-agent
LogsDirectoryMode=0750
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadOnlyPaths=$install_dir
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF

install -d -o root -g root -m 0755 "$install_dir"
install -o root -g root -m 0755 "$tmp_dir/$asset" "$install_dir/baize-agent"
install -o root -g root -m 0600 "$tmp_dir/config.yml" "$install_dir/config.yml"
install -o root -g root -m 0600 "$tmp_dir/baize-agent.service" /etc/systemd/system/baize-agent.service
systemctl daemon-reload
systemctl enable --now baize-agent
echo "baize-agent installed for $robot_code ($robot_uuid); model $robot_model"
