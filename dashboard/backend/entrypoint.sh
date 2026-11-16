#!/bin/sh
set -eu

data_dir=/dashboard/data
config_path="$data_dir/config.yaml"
template_path=/opt/baize/dashboard/config.yaml.example

mkdir -p "$data_dir"
if [ "$(id -u)" -eq 0 ]; then
  chown baize:baize "$data_dir"
  exec su-exec baize:baize "$0" "$@"
fi
if [ ! -e "$config_path" ]; then
  cp "$template_path" "$config_path"
  chmod 0600 "$config_path"
fi

exec /opt/baize/dashboard/bin/baize --config "$config_path" "$@"
