# EchoBot

EchoBot 由机器人端 Agent 和管理 Dashboard 组成。Agent 负责采集状态并向 Dashboard 上报；Dashboard 负责查看机器人、保存备注和分发 Agent 更新。

## 目录

- `/opt/echobot/agent`：Agent 二进制、配置和 systemd 服务。
- `/opt/echobot/dashboard`：Dashboard 镜像中的程序、静态页面和数据。
- `shared/model/`：Agent 与 Dashboard 之间的遥测数据契约。

## 启动 Dashboard

Dashboard 默认以 root 用户运行，数据保存在 `/opt/echobot/dashboard/data`。先准备两个长度不少于 12 个字符的随机值：一个给 Agent，一个给 Dashboard 管理员。

```bash
docker pull chaeoi/echobot:latest
docker run -d --name echobot-dashboard \
  --restart unless-stopped \
  --user root \
  -p 8080:8080 \
  -e DASHBOARD_AGENT_TOKEN='replace-with-a-long-random-token' \
  -e DASHBOARD_ADMIN_PASSWORD='replace-with-a-long-admin-password' \
  -v echobot-dashboard-data:/opt/echobot/dashboard/data \
  chaeoi/echobot:latest
```

访问 `http://<dashboard-host>:8080`。需要 HTTPS 时，同时设置 `DASHBOARD_TLS_CERT` 和 `DASHBOARD_TLS_KEY`，并把证书目录只读挂载到容器中。

Dashboard 环境变量：

- `DASHBOARD_AGENT_TOKEN`：Agent 上报和更新请求使用的 Bearer token。
- `DASHBOARD_ADMIN_PASSWORD`：Dashboard 管理员登录密码。
- `DASHBOARD_LISTEN`：监听地址，默认 `:8080`。
- `DASHBOARD_DATA_DIR`：持久化目录，默认 `/opt/echobot/dashboard/data`。
- `DASHBOARD_FRONTEND_DIR`：静态页面目录，默认 `/opt/echobot/dashboard/frontend`。
- `DASHBOARD_TLS_CERT`、`DASHBOARD_TLS_KEY`：同时设置时启用内置 HTTPS。
- `DASHBOARD_ALLOWED_ORIGIN`：只有跨域访问 API 时才需要设置。

## 部署 Agent

在机器人上准备 `linux/arm64` 或 `linux/amd64` 的 Agent 文件，并放入 `/opt/echobot/agent`：

```bash
sudo install -d -o ubuntu -g ubuntu -m 0750 /opt/echobot/agent
sudo install -o ubuntu -g ubuntu -m 0755 echobot-agent-linux-arm64 /opt/echobot/agent/echobot-agent
sudo install -o ubuntu -g ubuntu -m 0600 agent/echobot.yml.example /opt/echobot/agent/echobot.yml
sudoedit /opt/echobot/agent/echobot.yml
```

配置中必须填写每台机器人独有的 UUID、机器人编码、Dashboard 地址和与容器一致的 Agent token。内置硬件档案为 `2m_v0.1.2`；通常只需要调整身份字段和 `bms.publish_ros2` 等开关。

检查配置并启动服务：

```bash
sudo -u ubuntu /opt/echobot/agent/echobot-agent \
  -config /opt/echobot/agent/echobot.yml -check-config
sudo install -m 0644 agent/deploy/echobot-agent.service /etc/systemd/system/echobot-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now echobot-agent
```

## 电池支持

当前仅支持 `YY-BCU14H-MOS-24S100A` BMS。它用于 24 串、100 A 级别的锂电池包，并通过 CAN 提供电压、电流、SOC、单体统计、温度、SOH、循环次数和故障状态。电池包须使用该型号 BMS 并按现场 CAN 接口配置；Agent 不会修改 CAN 网卡参数。

## 查看与升级

登录 Dashboard 后，在机器人列表中查看主机、CPU、内存、磁盘、GPU、电机只读状态和 BMS 数据。版本页面可上传对应平台的 Agent 文件并校验 SHA-256，也可以为单台机器人指定版本；启用自动更新的 Agent 会按配置周期检查并替换自身。

Agent 只读取已存在的 ROS2 状态 topic，不连接电机 CAN，不发送电机控制、查询或配置指令。
