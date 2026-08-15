# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Agent 负责采集状态并向 Dashboard 上报；Dashboard 负责查看机器人、保存备注和分发 Agent 更新。公开只读 API 可以直接嵌入其他站点，管理 API 使用独立会话和强制改密流程。

## 目录

- `/opt/baize/agent`：Agent 二进制、配置和 systemd 服务。
- `/opt/baize/dashboard/config.yml`：Dashboard 配置文件。
- `/opt/baize/dashboard/data/control`：控制库，保存机器人身份、备注、目标版本和管理员账号。
- `/opt/baize/dashboard/data/history`：独立历史库，保存可按保留期清理的监控采样；迁移时可以只复制 `control`。
- `shared/model/`：Agent 与 Dashboard 之间的遥测数据契约。

## 启动 Dashboard

推荐使用 host 网络，监听地址和端口由 `dashboard/config.yml` 的 `dashboard.listen` 控制。首次运行不需要准备配置文件，容器会在宿主机目录中自动生成配置、创建默认用户 `admin`，并生成 Agent token 和 JWT secret：

```bash
sudo install -d -m 0750 /opt/baize/dashboard
sudo install -d -m 0750 /opt/baize/dashboard/data
docker pull chaeoi/baize:latest
docker run -d --name baize \
  --restart unless-stopped \
  --network host \
  -v /opt/baize/dashboard:/config \
  -v /opt/baize/dashboard/data:/data \
  chaeoi/baize:latest
```

默认管理员密码为 `Baize@Admin1`，第一次登录会强制修改；Agent token 和 JWT secret 会自动生成并写回配置。Agent 配置中的 `agent.token` 使用配置里的同一个 token。

访问 `http://<dashboard-host>:8080`。需要修改监听端口时，编辑宿主机 `/opt/baize/dashboard/config.yml` 的 `dashboard.listen` 后重启容器。

Dashboard 配置项：

- `dashboard.agent_token`：Agent 上报和更新请求使用的 Bearer token。
- `dashboard.admin_user`：管理员用户名，默认 `admin`。
- `dashboard.admin_password`：Dashboard 管理员登录密码。
- `dashboard.password_change_required`：是否要求首次登录修改初始密码。
- `dashboard.jwt_secret`：签发 Dashboard 登录 JWT 的密钥，缺少时自动生成。
- `dashboard.listen`：监听地址，默认 `:8080`。
- `dashboard.data_dir`：控制库目录，默认 `/data/control`。
- `dashboard.history_data_dir`：历史库目录，默认 `/data/history`。
- `dashboard.history_retention`：历史保留时间，默认 90 天。
- `dashboard.frontend_dir`：静态页面目录，默认 `/opt/baize/dashboard/frontend`。

`dashboard/config.yml` 是配置模板；需要预设用户名和密码时，将它复制到 `/opt/baize/dashboard/config.yml` 后编辑即可。

公开数据接口为 `GET /api/v1/robots`、`GET /api/v1/robots/{public_id}/history` 和 `wss://<host>/api/v1/ws/robots`。它们只返回公开摘要、曲线和电机运行量，不返回 UUID、主机名、操作系统、CAN 接口或管理配置；REST 接口允许跨域只读嵌入。

## 部署 Agent

在机器人上可以用下面一条命令部署最新版本 Agent。它会按 CPU 架构下载静态二进制、生成 `config.yml`、安装 systemd 服务并立即启动：

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/baize/main/agent/deploy/install.sh | \
  sudo bash -s -- \
  --dashboard-url http://<dashboard-host>:8080 \
  --token 'replace-with-the-same-token-as-dashboard-config' \
  --robot-code M99
```

未传 `--uuid` 时脚本会生成一个新的 UUID；同一台机器人重新部署前请保留原有 `/opt/baize/agent/config.yml`。也可以显式传入 `--uuid` 和 `--robot-model`。

手动部署时，在机器人上准备 `linux/arm64` 或 `linux/amd64` 的 Agent 文件，并放入 `/opt/baize/agent`：

```bash
sudo install -d -o ubuntu -g ubuntu -m 0750 /opt/baize/agent
sudo install -d -o root -g root -m 0755 /opt/baize/agent/profiles
sudo install -o ubuntu -g ubuntu -m 0755 baize-agent-linux-arm64 /opt/baize/agent/baize-agent
sudo install -o root -g ubuntu -m 0640 agent/config.yml.example /opt/baize/agent/config.yml
sudo install -o root -g ubuntu -m 0640 agent/profiles/2m_v0.1.2.yml /opt/baize/agent/profiles/
sudoedit /opt/baize/agent/config.yml
```

配置中必须填写每台机器人独有的 UUID、机器人编码、Dashboard 地址和与容器一致的 Agent token。硬件档案由 `/opt/baize/agent/profiles/2m_v0.1.2.yml` 这类型号文件决定；通常只需要调整身份字段和 `bms.publish_ros2` 等开关。

检查配置并启动服务：

```bash
sudo -u ubuntu /opt/baize/agent/baize-agent \
  -config /opt/baize/agent/config.yml -check-config
sudo install -m 0644 agent/deploy/baize-agent.service /etc/systemd/system/baize-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now baize-agent
```

需要在没有真实电机或 BMS 数据时做联调，可以在 ROS2 机器人上运行仓库内的动态模拟器。它会持续发布 32 个电机的 JointState 和 BatteryState：

```bash
source /opt/ros/humble/setup.bash
python3 agent/deploy/simulate_ros2.py --rate 5
```

## 电池支持

型号文件可以选择标准 ROS2 `sensor_msgs/msg/BatteryState` 话题，也可以选择声明式 SocketCAN 查询。示例 profile 为 `YY-BCU14H-MOS-24S100A`，通过 CAN 提供电压、电流、SOC、单体统计、温度、SOH、循环次数和故障状态；Agent 不会修改 CAN 网卡参数。

## 查看与升级

登录 Dashboard 后，在机器人列表中查看主机、CPU、内存、磁盘、GPU、电机只读状态和 BMS 数据。版本页面可上传对应平台的 Agent 文件并校验 SHA-256，也可以为单台机器人指定版本；启用自动更新的 Agent 会按配置周期检查并替换自身。

Agent 的 ROS2 adapter 只读取已存在的状态 topic；CAN adapter 只发送型号 profile 明确声明的查询帧，不发送电机控制或配置指令。profile 可以声明 CAN 请求/响应 ID、字段偏移、端序和缩放，但不能执行任意命令。
