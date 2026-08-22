# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Dashboard 的控制数据保存在独立的
SQLite 数据库 `/dashboard/data/control/control.db`；监控历史保存在嵌入式 VictoriaMetrics TSDB
目录 `/dashboard/data/history/host` 与 `/dashboard/data/history/motor`，不需要额外数据库容器。迁移时只复制
`control` 即可保留机器人身份、备注、管理员账号、发布版本和 Dashboard 密钥；历史目录可按
保留策略丢弃。旧版 `history.db` 不再读取或迁移。

主机、电池、GPU 和电机摘要按 `history_sample_interval`（默认 1 分钟）写入 `host`，
默认保留 90 天；500 Hz 电机原始位置、速度和转矩写入独立的 `motor` 时间序列，默认保留
2 分钟。长周期查询会覆盖完整时间范围降采样，短窗口电机查询保留原始采样点。

型号能力维护在 `shared/robotmodel/models.yml` 的单文件 YAML catalogue 中，GitHub
构建时同时嵌入 Agent 二进制和 Dashboard Docker：Agent 只读取 ROS2 状态话题，BMS CAN
查询由独立的 [`batcan`](https://github.com/chaeoi/batcan) 服务负责。部署仍使用
`config.yaml` 管理监听地址、身份和通用采集策略，但不允许通过 YAML 修改型号 profile。

## Dashboard

Dashboard 镜像自带配置样例。首次启动时会在数据卷中自动生成
`/dashboard/data/config.yaml`（已有文件会直接复用），并在
`/dashboard/data/control/control.db` 中生成 Agent token 和 JWT 密钥。默认管理员是
`admin`，默认密码 `Baize@Admin1`，首次登录后强制修改。生产配置应由 root 保存为 `0600`。

```bash
docker run -d \
  --name baize \
  --restart always \
  --network host \
  -v /opt/baize/dashboard/data:/dashboard/data \
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
的 Release 二进制。Agent 自带服务安装器，`service install` 会把当前二进制安装到
`/opt/baize/agent/baize-agent`，创建 systemd 单元，并生成由 root 所有、仅 Agent
服务账户可读的 `/opt/baize/agent/config.yml`。topic、电机清单、电池 ROS2 消息格式
均由二进制内置 profile 决定，配置文件不能覆盖。

现场配置用顶层 `model` 选择一个型号，型号文件不会复制到机器人：

```yaml
model: "2m_v0.1.2"
agent:
  uuid: "..."
  robot_code: "M99"
  dashboard_url: "https://baize.example.com"
  token: "..."
```

直接运行不带参数的安装命令会生成待填写的默认配置，但不会启动配置不完整的服务：

```bash
sudo ./baize-agent-linux-amd64 service install
sudoedit /opt/baize/agent/config.yml
sudo /opt/baize/agent/baize-agent service install
```

也可以在安装时一次传入完整配置，验证通过后服务会立即启动：

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/baize/main/agent/deploy/install.sh | \
  sudo sh -s -- \
  --dashboard-url http://<dashboard-host>:8080 \
  --token '<agent-token>' \
  --robot-code M99 \
  --robot-model 2m_v0.1.2
```

下载脚本只负责选择架构、校验 Release，然后把安装参数传给
`baize-agent service install`。指定 Release 可设置 `BAIZE_VERSION=v1.2.3`。
未传 `--uuid` 时 Agent 生成永久 UUID；也支持 `--force-config`。安装参数只用于写入 `config.yml`，不会成为第二个
运行时配置来源；已有有效配置默认保留，传入某个参数时只更新对应字段。`--force-config`
用于从默认模板重新生成配置。可用以下命令查看状态或卸载服务；卸载保留二进制和配置，
方便重新注册：

```bash
sudo /opt/baize/agent/baize-agent service install --uuid 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
sudo /opt/baize/agent/baize-agent service status
sudo /opt/baize/agent/baize-agent service uninstall
```

支持的模型、ROS2 topic、关节标签与电机元数据均编入 Agent；不支持的型号会在
安装校验阶段失败。Agent Release 内置按架构编译的 C++ `rclcpp` 订阅器，安装及
自动升级时释放到 Agent 的 systemd 状态目录。它长期监听 topic 并输出紧凑数据，
不依赖 Python、`rclpy` 或 `ros2 topic echo`：

- 电机：`/motor/q2w_upper_motor_joint_state`，`sensor_msgs/msg/JointState`
- 电池：`/batcan/data`，`diagnostic_msgs/msg/DiagnosticArray`

电池 topic 由独立 BMS 服务发布：

```bash
curl -fsSL https://raw.githubusercontent.com/chaeoi/batcan/main/deploy/install.sh | \
  sudo sh -s --
sudoedit /opt/batcan/config.yml
# 2m_v0.1.2 对应当前 Batcan 的 KVMS profile：
# profile: 98b8d1c1-6a34-45a4-9687-e9a09ef20204
sudo systemctl enable --now batcan
```

## 本地联调

在 ROS2 Humble 主机上运行动态模拟器，可持续发布 32 个电机与标准电池状态：

```bash
source /opt/ros/humble/setup.bash
python3 agent/deploy/simulate_ros2.py --rate 5
```
