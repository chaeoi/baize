# 型号能力架构

白泽采用“仓库 YAML 型号 catalogue + 构建时嵌入 + 统一遥测语义”，不让现场设备
依赖外置型号文件，也不让 Dashboard 或现场 YAML 理解厂商原始协议。

- `shared/robotmodel/models.yml` 使用与 batcan 相同的单文件、多 YAML 文档方式；每个
  `---` 文档对应一个机器人型号。Agent 与 Dashboard 构建时通过 `go:embed` 将同一份
  catalogue 编进二进制/Docker，现场只保留顶层 `model` 选择项。
- Agent 仅通过 C++ `rclcpp` helper 订阅 ROS2 标准
  `sensor_msgs/msg/JointState` 与 Batcan 发布的
  `diagnostic_msgs/msg/DiagnosticArray`。
- 每个支持的机器人型号在 YAML 中定义 topic、消息类型、关节标签和电机元数据；构建
  时校验后编入 Agent Release，安装时只选择顶层 `model`。
- BMS 的 SocketCAN 协议属于独立 `batcan` Release。Batcan 使用自己的
  `models/bms.yml` catalogue 和 UUID profile 选择；Baize Agent 不读取这些 CAN
  配置，也不打开 CAN socket，只读取 Batcan 发布的 DiagnosticArray topic。
- Dashboard 只消费统一的位置（rad）、速度（rad/s）、转矩（N m）和标准电池
  状态，因此新增型号不改变公开 API 或历史库。

这种边界与 ROS2 的 `JointState`/`DiagnosticArray` 以及商业机器人 SDK 的稳定状态
服务模型一致：监控系统读状态，不接管设备控制。

新增型号时，只需在 `shared/robotmodel/models.yml` 增加一个 YAML 文档，并补充单元测试
和实机只读验证；然后由 GitHub Actions 发布包含该型号的 Agent 和 Dashboard。运行旧版本的机器人会明确
拒绝未知型号，而不会猜测或发送未知总线帧。
