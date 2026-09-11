# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Dashboard 的控制数据保存在独立的
SQLite 数据库 `/dashboard/data/control/control.db`；监控历史保存在嵌入式 VictoriaMetrics TSDB
目录 `/dashboard/data/history/host` 与 `/dashboard/data/history/motor`，不需要额外数据库容器。迁移时只复制
`control` 即可保留机器人身份、备注、管理员账号和 Dashboard JWT 密钥；历史目录可按
保留策略丢弃。旧版 `history.db` 不再读取或迁移。

主机、电池、GPU 和电机摘要按 `history_sample_interval`（默认 2 秒，0.5 Hz）写入 `host`，
默认保留 90 天；500 Hz 电机原始位置、速度和转矩写入独立的 `motor` 时间序列，默认保留
2 分钟。面板显示采样率固定为：主机性能 0.5 Hz、全部电机 20 Hz、单个电机
500 Hz。历史范围决定时间跨度，接口会按对应采样率取样并覆盖完整范围；实时模式从点击后
收到的第一批数据开始持续累积，不限制固定点数窗口。

型号能力维护在 `shared/robotmodel/models.yml` 的单文件 YAML catalogue 中，GitHub
构建时同时嵌入 Agent 二进制和 Dashboard Docker：Agent 只读取 ROS2 状态话题，BMS CAN
查询由独立的 [`batcan`](https://github.com/chaeoi/batcan) 服务负责。部署仍使用
`config.yaml` 管理监听地址、Agent 连接密钥和通用采集策略，但不允许通过 YAML 修改型号 profile。

## Dashboard

Dashboard 镜像自带配置样例。首次启动时会在数据卷中自动生成
`/dashboard/data/config.yaml`（已有文件会直接复用），并在
`/dashboard/data/control/control.db` 中保存 JWT 密钥。唯一管理员账号固定为
`admin`，首次登录使用初始密码 `123456`，登录后强制修改。生产配置应由 root 保存为 `0600`。

`dashboard.agent_token` 可直接填写 Agent 使用的 Bearer 密钥，至少 12 个字符；留空时
Dashboard 会首次启动生成随机密钥并写回 `/dashboard/data/config.yaml`。Agent 密钥只有这一个
配置来源，修改配置后重启容器，所有 Agent 必须使用新的密钥。

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
为 `http://127.0.0.1:5037` 时，配置 `listen: "127.0.0.1:5037"`。JWT 密钥始终保存在
control DB；Agent token 始终来自 YAML。Agent 使用错误密钥时，Dashboard 会将来源 IP、
请求路径和错误密钥的短指纹写入容器日志，不会记录密钥明文。查看最近的失败连接：

```bash
docker logs --since 1h baize 2>&1 | grep 'invalid agent token'
```

持续观察新连接：

```bash
docker logs -f baize 2>&1 | grep 'invalid agent token'
```

日志中的 `remote_ip` 是直接 TCP 对端地址；若前面还有反向代理，默认看到的是代理 IP。

公开数据接口为 `GET /api/v1/robots`、`GET /api/v1/robots/{public_id}/history`
及 `wss://<host>/api/v1/ws/robots`。接口允许跨域只读嵌入，并且不暴露 UUID、
主机名、操作系统、总线元数据或管理配置。

机器人详情网址包含趋势筛选，例如
`/robot/{public_id}?view=single&range=realtime&motor=motor_id_01`。
`view` 支持 `host`、`motors`、`single`；主机 `range` 为小时数
`1`、`6`、`24`、`168`，电机为 `60`（最近 1 分钟）或 `realtime`。
全部电机通过 `metric=torque_nm|velocity_rad_per_sec|position_rad` 选择指标。
选择器和图表直接使用 ROS2 消息中的电机 ID，不显示关节别名。
从列表打开机器人默认查看主机最近 1 小时；点击其他趋势维度使用该维度默认范围。
刷新、分享链接及浏览器前进后退按网址恢复筛选，重新进入实时模式从当次进入开始累积。
切换机器人、维度、范围或电机时会取消旧历史请求、清空旧图表数据并同步高频采样订阅。

## 原始转发与 MCAP 录制

Agent 持续订阅电机和电池 topic，直接获取 ROS2 序列化后的 CDR 字节，不解析字段、
计算摘要或抽样。主机/GPU 指标按 `agent.report_interval` 采集为一条 JSON 消息，
使用 `/baize/host` topic；三类数据经过同一条二进制分批、Zstandard 快速压缩链路
发送到 `POST /api/v1/raw`。批次携带机器人身份，每条消息携带时间戳。

Dashboard 后端解压和解析消息，生成摘要、曲线和历史数据。不录制时也持续转发，
因此曲线不依赖录制开关。电机和电池原始消息均逐条保留；面板显示与历史摘要的抽样
不会改变录制内容。

登录管理员账号并完成首次改密后，回到展示页点击“开始录制 / 停止录制”，Dashboard 后端将该机器人录制区间内收到的
全部原始数据写入压缩 MCAP，存储目录由 `dashboard.recording_dir` 指定，默认
`/dashboard/data/recordings`。浏览器只查询录制状态和下载文件，不在内存或 IndexedDB
中积累录制数据。录制独立于当前曲线筛选，刷新、关闭页面或切换机器人后仍继续，
回到该机器人页面可停止。下载按钮获取最近一次完成的录制，之前的文件保留在服务器目录。

MCAP 内的 ROS2 通道保留原始 CDR 和完整 `ros2msg` 消息定义，主机通道使用 JSON 和
JSON Schema，并附带机器人 UUID、编码和型号。可交给支持 CDR、JSON 的 MCAP 读取器
处理；在 PlotJuggler 中使用 MCAP 加载器及相应消息解析插件。它是混合消息编码的
MCAP，不声明只允许 CDR 的 `ros2` profile，不能把该文件等同于 `ros2 bag record` 的输出。

Agent 的 `raw_batch_interval` 默认 `2s`，控制批量发送延迟，最小 `250ms`；批次还会
按大小提前发送以限制内存。`raw_cache_bytes` 默认 `268435456`（256 MiB），按压缩后的
文件字节数限制本地缓存，最小 1 MiB。缓存位于 systemd `StateDirectory` 下的
`raw-outbox`，直接运行时位于用户缓存目录的 `baize-agent/<uuid>/raw-outbox`。
网络正常与断网使用同一队列，成功确认后删除对应批次；满时删除最旧批次，保留新数据。
队列使用内存 FIFO 索引，运行期间无需反复扫描缓存目录，HTTP 等待不阻塞采集。

停止录制后显示“等待缓存补齐”，收到跨过停止时间的有序批次后才完成文件，避免遗漏
断网期间仍在缓存中的消息。超过缓存上限而淘汰的数据不会恢复；若 Agent 一直离线，
则会一直等待恢复连接。录制边界使用 Agent 接收时间与 Dashboard 的开始/停止时间，
两端系统时钟应保持同步；ROS header 时间另存为 MCAP 的发布时间，CDR 内容不改写。
Dashboard 录制时先将区间内的压缩批次写入 `.mcap.journal` 并同步到磁盘，再确认接收；
MCAP 正常封存后删除对应日志。异常退出后，下次启动自动将日志恢复成同名 MCAP，
仅忽略末尾尚未写完的批次；完整批次损坏会报错并保留原件。日志临时占用额外磁盘空间，
不增加浏览器内存或 Agent 网络流量。写盘失败会中断本次录制并在页面显示错误，
实时曲线继续工作；修复磁盘问题后可重试录制，失败期间的数据不属于成功录制内容。
Agent 序号按块持久化预留，不依赖系统时间，重启跳过未使用序号；自动更新先下载、
校验候选版本，再停止采集并保存剩余批次后替换进程。

本次更新移除了旧 JSON 遥测接口和 CSV 录制链路，需要同时更新 Agent 与 Dashboard。

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
`baize-agent service install`。安装后的 Agent 按 `update.check_interval` 定时直接检查
GitHub Release，并在校验通过后自动升级，不依赖 Dashboard 在线或参与发布。
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

在 ROS2 Humble 主机上运行动态模拟器，可持续发布 32 个电机与 `DiagnosticArray` 电池消息：

```bash
source /opt/ros/humble/setup.bash
python3 agent/deploy/simulate_ros2.py --rate 500 --battery-rate 20
```
