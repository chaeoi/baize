# EchoBot

EchoBot 由机器人端 Agent 和管理 Dashboard 组成。Agent 负责采集状态并向 Dashboard 上报；Dashboard 负责查看机器人、保存备注和分发 Agent 更新。

## 目录

- `/opt/echobot/agent`：Agent 二进制、配置和 systemd 服务。
- `/opt/echobot/dashboard/config.yml`：Dashboard 配置文件。
- `/opt/echobot/dashboard/data`：Dashboard 在宿主机上的持久化数据目录。
- `shared/model/`：Agent 与 Dashboard 之间的遥测数据契约。

## 启动 Dashboard

推荐使用 host 网络，监听地址和端口由 `dashboard/config.yml` 的 `dashboard.listen` 控制。首次运行不需要准备配置文件，容器会在宿主机目录中自动生成配置、创建默认用户 `admin`，并随机生成管理员密码、Agent token 和 JWT secret：

```bash
sudo install -d -m 0750 /opt/echobot/dashboard
sudo install -d -m 0750 /opt/echobot/dashboard/data
docker pull chaeoi/echobot:latest
docker run -d --name echobot \
  --restart unless-stopped \
  --network host \
  -v /opt/echobot/dashboard:/config \
  -v /opt/echobot/dashboard/data:/data \
  chaeoi/echobot:latest
```

使用 `docker logs echobot` 查看首次生成的管理员密码和 Agent token；Agent 配置中的 `agent.token` 使用日志中同一个 token。已有 `config.yml` 时会沿用已有值，缺少的密钥会自动补齐并写回。

访问 `http://<dashboard-host>:8080`。需要修改监听端口时，编辑宿主机 `/opt/echobot/dashboard/config.yml` 的 `dashboard.listen` 后重启容器。

Dashboard 配置项：

- `dashboard.agent_token`：Agent 上报和更新请求使用的 Bearer token。
- `dashboard.admin_user`：管理员用户名，默认 `admin`。
- `dashboard.admin_password`：Dashboard 管理员登录密码。
- `dashboard.jwt_secret`：签发 Dashboard 登录 JWT 的密钥，缺少时自动生成。
- `dashboard.listen`：监听地址，默认 `:8080`。
- `dashboard.data_dir`：容器内持久化目录，默认 `/data`。
- `dashboard.frontend_dir`：静态页面目录，默认 `/opt/echobot/dashboard/frontend`。

`dashboard/config.yml` 是配置模板；需要预设用户名和密码时，将它复制到 `/opt/echobot/dashboard/config.yml` 后编辑即可。

## 部署 Agent

在机器人上可以用下面一条命令部署最新版本 Agent。它会按 CPU 架构下载静态二进制、生成 `config.yml`、安装 systemd 服务并立即启动：

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/echobot/main/agent/deploy/install.sh | \
  sudo bash -s -- \
  --dashboard-url http://<dashboard-host>:8080 \
  --token 'replace-with-the-same-token-as-dashboard-config' \
  --robot-code M99
```

未传 `--uuid` 时脚本会生成一个新的 UUID；同一台机器人重新部署前请保留原有 `/opt/echobot/agent/config.yml`。也可以显式传入 `--uuid` 和 `--robot-model`。

手动部署时，在机器人上准备 `linux/arm64` 或 `linux/amd64` 的 Agent 文件，并放入 `/opt/echobot/agent`：

```bash
sudo install -d -o ubuntu -g ubuntu -m 0750 /opt/echobot/agent
sudo install -o ubuntu -g ubuntu -m 0755 echobot-agent-linux-arm64 /opt/echobot/agent/echobot-agent
sudo install -o ubuntu -g ubuntu -m 0600 agent/config.yml.example /opt/echobot/agent/config.yml
sudoedit /opt/echobot/agent/config.yml
```

配置中必须填写每台机器人独有的 UUID、机器人编码、Dashboard 地址和与容器一致的 Agent token。内置硬件档案为 `2m_v0.1.2`；通常只需要调整身份字段和 `bms.publish_ros2` 等开关。

检查配置并启动服务：

```bash
sudo -u ubuntu /opt/echobot/agent/echobot-agent \
  -config /opt/echobot/agent/config.yml -check-config
sudo install -m 0644 agent/deploy/echobot-agent.service /etc/systemd/system/echobot-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now echobot-agent
```

## 电池支持

当前仅支持 `YY-BCU14H-MOS-24S100A` BMS。它用于 24 串、100 A 级别的锂电池包，并通过 CAN 提供电压、电流、SOC、单体统计、温度、SOH、循环次数和故障状态。电池包须使用该型号 BMS 并按现场 CAN 接口配置；Agent 不会修改 CAN 网卡参数。

## 查看与升级

登录 Dashboard 后，在机器人列表中查看主机、CPU、内存、磁盘、GPU、电机只读状态和 BMS 数据。版本页面可上传对应平台的 Agent 文件并校验 SHA-256，也可以为单台机器人指定版本；启用自动更新的 Agent 会按配置周期检查并替换自身。

Agent 只读取已存在的 ROS2 状态 topic，不连接电机 CAN，不发送电机控制、查询或配置指令。
