"""Small, persistent ROS2 subscriber used by the Baize Agent.

The ROS2 CLI is intentionally not used here.  ``ros2 topic echo`` converts
every message to a human-readable YAML document, which is needlessly expensive
for a 500 Hz JointState stream.  This helper serializes only the fields the
Agent consumes and keeps one rclpy process alive for the lifetime of a stream.
"""

import argparse
import json
import math
import sys
import time

import rclpy
from rclpy.node import Node
from rclpy.qos import QoSProfile, qos_profile_sensor_data
from rclpy.executors import ExternalShutdownException


def finite(value):
    value = float(value)
    return value if math.isfinite(value) else 0.0


def emit(payload):
    output = json.dumps(payload, separators=(",", ":"), ensure_ascii=True)
    sys.stdout.buffer.write(output.encode("utf-8"))
    sys.stdout.buffer.write(b"\n")
    sys.stdout.buffer.flush()


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
        stamp = getattr(message.header, "stamp", None)
        stamp_ns = 0
        if stamp is not None:
            stamp_ns = int(stamp.sec) * 1_000_000_000 + int(stamp.nanosec)
        if stamp_ns <= 0:
            stamp_ns = time.time_ns()
        emit(
            {
                "type": "motor",
                "stamp_ns": stamp_ns,
                "name": list(message.name),
                "position": [finite(value) for value in message.position],
                "velocity": [finite(value) for value in message.velocity],
                "effort": [finite(value) for value in message.effort],
            }
        )

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
    finally:
        if node is not None:
            node.destroy_node()
        if rclpy.ok():
            rclpy.shutdown()


if __name__ == "__main__":
    main()
