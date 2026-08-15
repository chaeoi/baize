# 型号 Profile 架构评估

## 产品结论

白泽采用“统一遥测语义 + 受控 transport adapter + 声明式型号 profile”，而不是让 Dashboard 或业务代码理解每家厂商的原始协议。

- 统一语义：主机性能、电池状态、电机位置（rad）、速度（rad/s）、转矩（N·m）。
- transport adapter：目前支持 ROS2 topic 和只读 SocketCAN query。
- 型号 profile：选择 adapter，并声明 topic、消息类型或 CAN 请求/响应、偏移、端序、缩放、故障位和关节元数据。
- Dashboard：只消费统一语义，因此新增机器人型号不需要修改页面、公开 API 或历史库。

这种结构与主流机器人生态的边界一致：ROS 2 使用标准 `JointState` / `BatteryState` 消息；Unitree SDK2 将 DDS 通信与低层消息结构分开；Spot SDK 也由 robot-state 服务向上提供稳定状态模型；Foxglove 等可观测性产品消费标准 topic/消息而不接管设备控制。

参考：

- [ROS 2 JointState](https://docs.ros.org/en/rolling/p/sensor_msgs/msg/JointState.html)
- [ROS 2 BatteryState](https://docs.ros.org/en/rolling/p/sensor_msgs/msg/BatteryState.html)
- [Unitree SDK2](https://github.com/unitreerobotics/unitree_sdk2)
- [Boston Dynamics Spot SDK](https://github.com/boston-dynamics/spot-sdk)
- [Foxglove](https://github.com/foxglove/studio)

## 为什么不使用任意脚本

如果 profile 可以直接写 shell、Python 或任意表达式，它就同时获得主机执行和总线控制能力，无法审计。白泽只接受枚举过的 `source`、固定字段类型和最多 8 字节的 CAN 请求；ROS 命令由程序模板构造，topic 和消息类型经过格式校验及 shell quoting。

SocketCAN 无法从帧内容自动判断“查询”还是“控制”，所以 profile 本身仍属于受信任的部署资产：它随 Agent 版本发布，由管理员安装，服务进程以只读方式挂载。新增 profile 必须经过设备协议评审和实机只读验证。

## 扩展一个型号

1. 在 `agent/profiles/<robot_model>.yml` 新增 profile。
2. 优先使用机器人已有的标准 ROS2 状态 topic；只有 SDK 不提供稳定状态 topic 时才使用 CAN query。
3. CAN 字段必须写明请求 ID、响应 ID、offset、length、encoding、endian、scale 和 bias。
4. 用记录的响应帧为字段解码增加单元测试，再在隔离 CAN 总线上验证不会改变设备状态。
5. 运行 `baize-agent -check-config`、`go test ./...` 和 Dashboard 端到端测试。

## 后续兼容方向

如果接入厂商 SDK，不应把 SDK 类型传到 Dashboard。应新增 `unitree_dds`、`spot_grpc` 等受控 adapter，并继续输出现有统一语义。高频原始数据适合写入 ROS bag/MCAP；白泽历史库保留面向运维的降采样指标，不替代控制器日志或故障取证数据。
