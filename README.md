# 白泽 Baize

白泽由机器人端 Agent 和管理 Dashboard 组成。Dashboard 的控制数据保存在独立的
SQLite 数据库 `/dashboard/data/control/control.db`；监控历史保存在嵌入式 VictoriaMetrics TSDB
目录 `/dashboard/data/history/host` 与 `/dashboard/data/history/motor`，不需要额外数据库容器。
`control` 保存机器人身份、备注、管理密码和 Dashboard JWT 密钥；历史目录可按保留策略丢弃。

主机、电池、GPU 和电机摘要按 `history_sample_interval`（默认 2 秒，0.5 Hz）写入 `host`，
默认保留 90 天；500 Hz 电机原始位置、速度和转矩写入独立的 `motor` 时间序列，默认保留
2 分钟。面板显示采样率固定为：主机性能 0.5 Hz、全部电机 20 Hz、单个电机
500 Hz。历史范围决定时间跨度，接口会按对应采样率取样并覆盖完整范围；实时模式从点击后
收到的第一批数据开始持续累积，不限制固定点数窗口。

型号能力维护在 `shared/robotmodel/models.yml` 的单文件 YAML catalogue 中，GitHub
构建时同时嵌入 Agent 二进制和 Dashboard Docker：Agent 只读取 ROS2 状态话题，BMS CAN
查询由独立的 `batcan` 服务负责。部署仍使用
`config.yaml` 管理监听地址、Agent 连接密钥和通用采集策略，但不允许通过 YAML 修改型号 profile。

## Dashboard

Dashboard 镜像自带配置样例。首次启动时会在数据卷中自动生成
`/dashboard/data/config.yaml`（已有文件会直接复用），并在
`/dashboard/data/control/control.db` 中保存 JWT 密钥。管理登录只需密码，首次使用初始密码 `123456`，登录后强制修改。
新密码至少 12 个字符，UTF-8 编码不超过 72 字节。Dashboard 与哪吒相同，
默认以容器内 root 运行，配置权限为 `0600`。前端资源嵌入 Dashboard 二进制。

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

管理后台“设备管理”页顶部的“安装 Agent”按钮会读取同一个 token，按所选型号与设备编号
生成一键安装命令并写入剪贴板。

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
文件字节数限制本地缓存，最小 1 MiB。缓存位于配置文件所在目录的
`data/raw-outbox`，默认 `/opt/baize/agent/data/raw-outbox`。
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
校验候选程序及当前配置，再停止采集并保存剩余批次，原子替换当前二进制后退出，
由 systemd 自动启动新程序。替换前的下载或验证失败不会中断采集。

## Agent

GitHub Actions 构建 Linux AMD64/ARM64 静态 Agent；机器人只下载经 SHA-256 校验
的 Release 二进制。`service install` 将程序安装到 `/opt/baize/agent/baize-agent`，
写入 `/opt/baize/agent/config.yml`，创建以 root 运行的 systemd 服务。
安装目录为 `root:root`、权限 `0755`，配置为 `root:root`、权限 `0600`。
服务直接运行这个二进制，安装、版本查询和自动更新使用同一个文件。
topic、电机清单、电池 ROS2 消息格式均由二进制内置 profile 决定，配置文件不能覆盖。

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
curl -fsSL https://gitwarp.canghai.org/raw.githubusercontent.com/chaeoi/baize/main/agent/deploy/install.sh | \
  sudo sh -s -- \
  --dashboard-url http://<dashboard-host>:8080 \
  --token '<agent-token>' \
  --robot-code M99 \
  --robot-model 2m_v0.1.2
```

需要真正一条命令完成安装时，可把四个参数放入环境变量后执行：

```bash
curl -fsSL https://gitwarp.canghai.org/raw.githubusercontent.com/chaeoi/baize/main/agent/deploy/install.sh | \
  sudo env \
  BAIZE_DASHBOARD_URL=http://<dashboard-host>:8080 \
  BAIZE_AGENT_TOKEN='<agent-token>' \
  BAIZE_ROBOT_CODE=M99 \
  BAIZE_ROBOT_MODEL=2m_v0.1.2 \
  sh
```

下载脚本只负责选择架构、校验 Release，然后把安装参数传给
`baize-agent service install`。安装后的 Agent 按 `update.check_interval` 定时直接检查
GitHub 最新成功发布的 Release（默认经 GitWarp 加速，启动约 5 秒后首次检查、默认每 24 小时），
先读取发布附件 `VERSION`，按年、月、序号比较版本。只有远端版本更高时，才从该版本的固定标签
下载 `SHA256SUMS` 和二进制，校验 SHA-256、程序版本及当前配置后升级。相同或较低版本不下载程序。
尚未构建成功并发布的源码提交不会安装到机器人上，更新不依赖 Dashboard 在线或参与发布。

正式版本采用 `年.月.发布序号`，从 `2026.11.1` 开始，同月后续版本为 `2026.11.2`、
`2026.11.3`。每次发布创建新 tag，同一天也使用不同序号；已发布的版本不覆盖。
推送版本 tag 会构建同版本的 Agent Release 和 Dashboard 镜像，普通分支提交不直接发布：

```bash
git tag -a --cleanup=verbatim 2026.11.2
git push origin 2026.11.2
```

附注 tag 中填写本次发布的主要变化。发布页会使用这段说明，并附上安装入口、架构下载、
SHA-256 校验和及相邻版本的变更记录。

未传 `--uuid` 时 Agent 生成永久 UUID；也支持 `--force-config`。安装参数只用于写入 `config.yml`，不会成为第二个
运行时配置来源；已有有效配置默认保留，传入某个参数时只更新对应字段。`--force-config`
用于从默认模板重新生成配置。可用以下命令查看状态或卸载服务；卸载会删除二进制、配置、
本地缓存与日志，并注销服务，再次安装将生成新的 UUID：

```bash
sudo /opt/baize/agent/baize-agent service install --uuid 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
sudo /opt/baize/agent/baize-agent service status
sudo /opt/baize/agent/baize-agent service restart
sudo /opt/baize/agent/baize-agent service update
sudo /opt/baize/agent/baize-agent service uninstall
```

支持的模型、ROS2 topic、关节标签与电机元数据均编入 Agent；不支持的型号会在
安装校验阶段失败。Agent Release 内置按架构编译的 C++ `rclcpp` 订阅器，从匿名内存
启动并长期监听 topic，ROS2 UDP 配置同样由程序直接加载，不依赖 Python、`rclpy`
或 `ros2 topic echo`：

- 电机：`/motor/q2w_upper_motor_joint_state`，`sensor_msgs/msg/JointState`
- 电池：`/batcan/data`，`diagnostic_msgs/msg/DiagnosticArray`

电池 topic 由独立 BMS 服务发布，仓库地址：
`https://github.com/chaeoi/batcan`

## 部署目录与服务管理

两个客户端都采用单二进制和配置的结构，以 root 运行；服务直接读取同一份配置，
自动更新替换正在使用的二进制，随后退出，由 systemd 的 `Restart=always` 拉起。
安装和服务管理命令支持 `install`、`uninstall`、`start`、`stop`、`restart`、`status`、`update`。
更新协调锁位于 `/tmp`，程序与配置目录不保存第二份工作程序。

```text
服务端宿主机                    Dashboard 容器
/opt/baize/dashboard/data/      /dashboard/
├── config.yaml                ├── app          # 内嵌前端
├── control/control.db         ├── config.yaml.example
├── history/host/              └── data/        # 宿主机数据卷
├── history/motor/              /entrypoint.sh
└── recordings/

机器人
/opt/baize/agent/
├── baize-agent
├── config.yml
└── data/
    ├── raw-outbox/             # 待确认的原始批次与序号
    └── ros/                    # ROS2 日志
/opt/batcan/
├── batcan
├── config.yml
└── data/ros/
/etc/systemd/system/
├── baize-agent.service
└── batcan.service
```

Agent 与 batcan 在进程启动时直接加载内置 Fast DDS UDP 配置，只使用回环地址，
避免与其他用户启动的 ROS2 节点共享内存时出现权限冲突。磁盘上没有额外 XML 配置文件。
CAN 接口应为 BMS 专用接口，batcan 的自动探测会调整该接口的波特率。
运行日志通过 `journalctl -u baize-agent`、`journalctl -u batcan` 查看，
Dashboard 日志通过 `docker logs baize` 查看。

Dashboard 使用镜像更新：拉取新镜像后按上面的 Docker 命令重新创建容器，复用同一数据卷。

## 本地联调

在 ROS2 Humble 主机上运行动态模拟器，可持续发布 32 个电机与 `DiagnosticArray` 电池消息：

```bash
source /opt/ros/humble/setup.bash
python3 agent/deploy/simulate_ros2.py --rate 500 --battery-rate 20
```
