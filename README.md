# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Dashboard 的控制数据与监控历史
分别存放在独立 SQLite 数据库中：迁移时只复制 `control` 即可保留机器人身份、
备注、管理员账号、发布版本和 Dashboard 密钥；`history` 可按保留策略丢弃。

型号能力随 GitHub Release 编入二进制：Agent 只读取 ROS2 状态话题，BMS CAN
查询由独立的 [`batcan`](https://github.com/chaeoi/batcan) 服务负责。
项目不再下载外置型号 profile，也不使用 YAML 配置文件。

## Dashboard

Dashboard 镜像自带运行配置，首次启动时会在 `/data/control/control.db` 中生成
Agent token 和 JWT 密钥。默认管理员是 `admin`，默认密码 `Baize@Admin1`，首次
登录后强制修改。

```bash
sudo install -d -m 0750 /opt/baize/dashboard/data
docker pull chaeoi/baize:latest
docker run -d --name baize --restart unless-stopped \
  -p 8080:8080 \
  -v /opt/baize/dashboard/data:/data \
  chaeoi/baize:latest
```

登录并完成改密后，管理员可通过已认证接口读取用于安装 Agent 的 token：

```bash
curl --cookie 'baize_session=<session-cookie>' \
  http://<dashboard-host>:8080/api/v1/admin/agent-token
```

默认容器监听 `:8080`。部署环境确有必要时可用环境变量覆盖路径、监听地址和
Cookie TLS 标志：`BAIZE_LISTEN`、`BAIZE_DATA_DIR`、`BAIZE_HISTORY_DATA_DIR`、
`BAIZE_FRONTEND_DIR`、`BAIZE_COOKIE_SECURE`。它们不保存密码或 token。

公开数据接口为 `GET /api/v1/robots`、`GET /api/v1/robots/{public_id}/history`
及 `wss://<host>/api/v1/ws/robots`。接口允许跨域只读嵌入，并且不暴露 UUID、
主机名、操作系统、总线元数据或管理配置。

## Agent

GitHub Actions 构建 Linux AMD64/ARM64 静态 Agent；机器人只下载经 SHA-256 校验
的 Release 二进制。安装时会把每台机器人不可避免的身份信息写入 root-only 的
systemd unit 环境，二进制本身不依赖 `config.yml` 或外置型号文件。

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/baize/main/agent/deploy/install.sh | \
  sudo sh -s -- \
  --dashboard-url http://<dashboard-host>:8080 \
  --token '<agent-token>' \
  --robot-code M99 \
  --robot-model 2m_v0.1.2
```

未传 `--uuid` 时安装器生成永久 UUID。重新安装同一台机器人时显式传入原 UUID。
支持的模型、ROS2 topic、关节标签与电机元数据均编入 Agent；不支持的型号会在
安装校验阶段失败。Agent 仅通过不启用 ROS2 CLI daemon 的
`ros2 topic echo --no-daemon --once` 读取：

- 电机：`/motor/q2w_upper_motor_joint_state`，`sensor_msgs/msg/JointState`
- 电池：`/bms_can/battery_data`，`sensor_msgs/msg/BatteryState`

电池 topic 由独立 BMS 服务发布：

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/batcan/main/deploy/install.sh | \
  sudo sh -s -- --robot-model 2m_v0.1.2
```

## 本地联调

在 ROS2 Humble 主机上运行动态模拟器，可持续发布 32 个电机与标准电池状态：

```bash
source /opt/ros/humble/setup.bash
python3 agent/deploy/simulate_ros2.py --rate 5
```
