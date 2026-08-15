# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Dashboard 的控制数据与监控历史
分别存放在独立 SQLite 数据库中：迁移时只复制 `control` 即可保留机器人身份、
备注、管理员账号、发布版本和 Dashboard 密钥；`history` 可按保留策略丢弃。

型号能力随 GitHub Release 编入二进制：Agent 只读取 ROS2 状态话题，BMS CAN
查询由独立的 [`batcan`](https://github.com/chaeoi/batcan) 服务负责。部署仍使用
`config.yml` 管理监听地址、身份和通用采集策略，但不允许通过 YAML 修改型号 profile。

## Dashboard

Dashboard 镜像自带配置样例。首次启动时会在 `/data/control/control.db` 中生成
Agent token 和 JWT 密钥。默认管理员是 `admin`，默认密码 `Baize@Admin1`，首次
登录后强制修改。生产配置应由 root 保存为 `0600`。

```bash
sudo install -d -m 0750 /opt/baize/dashboard/data
sudo install -m 0600 dashboard/config.yml.example /opt/baize/dashboard/config.yml
docker pull chaeoi/baize:latest
docker run -d --name baize --restart unless-stopped \
  --network host \
  -v /opt/baize/dashboard/config.yml:/opt/baize/dashboard/config.yml:ro \
  -v /opt/baize/dashboard/data:/data \
  chaeoi/baize:latest
```

登录并完成改密后，管理员可通过已认证接口读取用于安装 Agent 的 token：

```bash
curl --cookie 'baize_session=<session-cookie>' \
  http://<dashboard-host>:8080/api/v1/admin/agent-token
```

修改 `dashboard.listen` 即可改变监听端口。例如 Cloudflare Tunnel 的本地 service
为 `http://127.0.0.1:5037` 时，配置 `listen: "127.0.0.1:5037"`。Agent token 与
JWT 密钥不写入 YAML，而是保存在 control DB。

公开数据接口为 `GET /api/v1/robots`、`GET /api/v1/robots/{public_id}/history`
及 `wss://<host>/api/v1/ws/robots`。接口允许跨域只读嵌入，并且不暴露 UUID、
主机名、操作系统、总线元数据或管理配置。

## Agent

GitHub Actions 构建 Linux AMD64/ARM64 静态 Agent；机器人只下载经 SHA-256 校验
的 Release 二进制。安装器会生成由 root 所有、仅 Agent 服务账户可读的 `/opt/baize/agent/config.yml`，其中
包含机器人身份、Dashboard 凭据和 `robot_model`。topic、电机清单、电池 ROS2
消息格式均由二进制内置 profile 决定，配置文件不能覆盖。

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
