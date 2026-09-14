# ROS 2 collector

The Agent keeps its main Go executable free of cgo and runs the embedded
`rclcpp` subscriber from anonymous memory. The helper uses `GenericSubscription` to subscribe to each configured topic and
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

The helper is compiled against ROS 2 Humble for each target architecture and
is embedded in the Agent release. The robot needs the ROS runtime and the
message packages, but does not need a C++ compiler or a source checkout. The
Agent keeps the helper in an anonymous memory file during startup and passes it
to each short-lived ROS process, so installation contains only the Agent
binaries and no persistent helper file. If ROS is missing, the host/system
collectors still run. Topic process failures are logged with bounded helper
diagnostics and retried after one second. Dashboard marks the topics offline
after the model's `read_timeout`, using receipt times rather than the ROS
simulation clock.
