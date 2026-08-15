# 电机采集说明

电机采集由 `agent/profiles/<robot_model>.yml` 选择 transport：

- `ros2_topic`：只读取已经存在的 `sensor_msgs/msg/JointState`，映射位置、速度和 effort/转矩。
- `can_query`：只发送 profile 明确声明的只读查询帧，按响应 ID、字节偏移、端序和缩放映射位置、速度和转矩。

两种 transport 都输出相同的 `MotorState` 语义模型，因此 Dashboard 和公开 API 不关心底层设备 SDK。profile 不能执行任意 shell 命令，也不能发送电机控制或配置帧；需要新增型号时只新增并审核 profile 文件。
