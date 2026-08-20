"""Small, persistent ROS2 subscriber used by the Baize Agent.

The ROS2 CLI is intentionally not used here.  ``ros2 topic echo`` converts
every message to a human-readable YAML document, which is needlessly expensive
for a 500 Hz JointState stream.  This helper serializes only the fields the
Agent consumes and keeps one rclpy process alive for the lifetime of a stream.
"""

import argparse
import json
import struct
import sys
import time

import rclpy
from rclpy.node import Node
from rclpy.qos import QoSProfile, qos_profile_sensor_data
from rclpy.executors import ExternalShutdownException


def emit(payload):
    output = json.dumps(payload, separators=(",", ":"), ensure_ascii=True)
    sys.stdout.buffer.write(output.encode("utf-8"))
    sys.stdout.buffer.write(b"\n")
    sys.stdout.buffer.flush()


def emit_motor(message):
    raw_names = message.name
    if raw_names == emit_motor.last_raw_names:
        names = emit_motor.last_names
    else:
        names = [str(value) for value in raw_names]
        emit_motor.last_raw_names = list(raw_names)
    count = len(names)
    if count > 1024:
        return
    include_names = names != emit_motor.last_names
    flags = 1 if include_names else 0
    stamp = getattr(message.header, "stamp", None)
    stamp_ns = 0 if stamp is None else int(stamp.sec) * 1_000_000_000 + int(stamp.nanosec)
    if stamp_ns <= 0:
        stamp_ns = time.time_ns()
    chunks = [struct.pack("<4sBBqH", b"BZM1", 1, flags, stamp_ns, count)]
    if include_names:
        for name in names:
            encoded = name.encode("utf-8")
            if len(encoded) > 65535:
                return
            chunks.append(struct.pack("<H", len(encoded)))
            chunks.append(encoded)
        emit_motor.last_names = names
    for values in (message.position, message.velocity, message.effort):
        if len(values) != count:
            return
        chunks.append(struct.pack("<" + "d" * count, *values))
    sys.stdout.buffer.write(b"".join(chunks))
    sys.stdout.buffer.flush()


emit_motor.last_names = None
emit_motor.last_raw_names = None


class Subscriber(Node):
    def __init__(self, topic, message_type):
        super().__init__("baize_agent_subscriber")
        self.message_type = message_type
        if message_type == "sensor_msgs/msg/JointState":
            from sensor_msgs.msg import JointState

            self.subscription = self.create_subscription(
                JointState, topic, self.on_joint_state, qos_profile_sensor_data
            )
        elif message_type == "diagnostic_msgs/msg/DiagnosticArray":
            from diagnostic_msgs.msg import DiagnosticArray

            # batcan uses the normal reliable ROS2 publisher QoS.
            self.subscription = self.create_subscription(
                DiagnosticArray, topic, self.on_diagnostic_array, QoSProfile(depth=10)
            )
        else:
            raise ValueError("unsupported ROS2 message type: " + message_type)

    def on_joint_state(self, message):
        emit_motor(message)

    def on_diagnostic_array(self, message):
        emit(
            {
                "type": "bms",
                "status": [
                    {
                        "name": status.name,
                        "message": status.message,
                        "hardware_id": status.hardware_id,
                        "values": [
                            {"key": item.key, "value": item.value}
                            for item in status.values
                        ],
                    }
                    for status in message.status
                ],
            }
        )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--topic", required=True)
    parser.add_argument("--message-type", required=True)
    args = parser.parse_args()

    rclpy.init(args=None)
    node = None
    try:
        node = Subscriber(args.topic, args.message_type)
        try:
            rclpy.spin(node)
        except ExternalShutdownException:
            pass
        except Exception:
            # rclpy may raise RCLError after its signal handler has already
            # invalidated the context. The process is shutting down normally.
            if rclpy.ok():
                raise
    finally:
        if node is not None:
            node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()


if __name__ == "__main__":
    main()
