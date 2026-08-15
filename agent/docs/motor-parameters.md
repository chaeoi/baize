# 电机采集说明

Baize Agent 只读取机器人已经发布的 ROS2
`sensor_msgs/msg/JointState`。每个型号的内置 profile 定义 topic、关节标签和
电机元数据，`position` 映射为弧度，`velocity` 映射为 rad/s，`effort` 映射为
N m 转矩。

Agent 不打开 SocketCAN、不发送电机指令，也不修改控制器或网络接口。需要支持
新型号时，更新并发布包含新 ROS2 topic 映射的 Agent 二进制。
