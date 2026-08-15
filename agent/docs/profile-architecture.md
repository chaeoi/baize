# 型号能力架构

白泽采用“统一遥测语义 + 编译内置型号能力”，不让 Dashboard 或现场 YAML 理解
厂商原始协议。

- Agent 仅订阅 ROS2 标准 `sensor_msgs/msg/JointState` 与
  `sensor_msgs/msg/BatteryState`。
- 每个支持的机器人型号把 topic、消息类型、关节标签和电机元数据编入 Agent
  Release；安装时只选择 `robot_model`。
- BMS 的 SocketCAN 协议属于独立 `batcan` Release。其本地文件也只选择
  型号，CAN 请求帧、响应 ID 和字段映射必须经过代码评审后随二进制发布。
- Dashboard 只消费统一的位置（rad）、速度（rad/s）、转矩（N m）和标准电池
  状态，因此新增型号不改变公开 API 或历史库。

这种边界与 ROS2 的 `JointState`/`BatteryState` 以及商业机器人 SDK 的稳定状态
服务模型一致：监控系统读状态，不接管设备控制。

新增型号时，在 Agent 和 BMS 项目中分别增加内置 profile、单元测试和实机只读
验证，然后由 GitHub Actions 发布包含该型号的版本。运行旧版本的机器人会明确
拒绝未知型号，而不会猜测或发送未知总线帧。
