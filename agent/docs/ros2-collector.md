# ROS 2 collector

The Agent keeps its main Go executable free of cgo and uses the embedded
`baize-ros2-subscriber` release asset as a separate `rclcpp` process. The
helper subscribes to the configured `JointState` or `DiagnosticArray` topic
and writes the compact stream consumed by the Agent.

The helper is compiled against ROS 2 Humble for each target architecture.
The robot needs the ROS runtime and the message packages, but does not need a
C++ compiler or a source checkout. Its dynamic dependencies can be checked with:

```sh
ldd /var/lib/baize-agent/baize-ros2-subscriber
```

The Agent copies the helper into `StateDirectory` during startup. This makes
an Agent self-update update the helper atomically as well. If ROS is missing,
the host/system collectors still run and the motor/BMS components report an
explicit unavailable error.
