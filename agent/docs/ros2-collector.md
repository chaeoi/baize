# ROS 2 collector

The Agent keeps its main Go executable free of cgo and uses the embedded
`baize-ros2-subscriber` release asset as a separate `rclcpp` process. The
helper uses `GenericSubscription` to subscribe to each configured topic and
forwards its original serialized CDR bytes. It does not deserialize ROS messages.
Each binary frame contains a `BZR1` magic, version, receipt timestamp, payload
length, and unchanged CDR payload. Motor subscriptions use sensor-data QoS with
a depth of 256; battery subscriptions use reliable QoS with a depth of 100.
Every received message is forwarded, including every battery message.

The Go process batches these frames with low-rate JSON host/GPU samples and
robot identity, then compresses once with Zstandard's fastest level. A bounded
disk FIFO retains compressed batches until the dashboard acknowledges them;
overflow evicts the oldest batch. HTTP sending runs independently from topic
collection. Dashboard owns ROS decoding, chart sampling, and MCAP recording.

The helper is compiled against ROS 2 Humble for each target architecture.
The robot needs the ROS runtime and the message packages, but does not need a
C++ compiler or a source checkout. Its dynamic dependencies can be checked with:

```sh
ldd /var/lib/baize-agent/baize-ros2-subscriber
```

The Agent copies the helper into `StateDirectory` during startup. This makes
an Agent self-update update the helper atomically as well. If ROS is missing,
the host/system collectors still run. Topic process failures are logged with
bounded helper diagnostics and retried after one second. Dashboard marks the
topics offline after the model's `read_timeout`, using receipt times rather than
the ROS simulation clock.
