#!/usr/bin/env python3
"""Publish deterministic, changing ROS2 telemetry for an integration test."""

import argparse
import math

import rclpy
from rclpy.node import Node
from sensor_msgs.msg import JointState
from diagnostic_msgs.msg import DiagnosticArray, DiagnosticStatus, KeyValue


class BaizeSimulation(Node):
    def __init__(self, motor_topic: str, battery_topic: str, rate: float, battery_rate: float) -> None:
        super().__init__("baize_telemetry_simulator")
        self.motor_publisher = self.create_publisher(JointState, motor_topic, 10)
        self.battery_publisher = self.create_publisher(DiagnosticArray, battery_topic, 100)
        self.joint_names = [f"motor_id_{index:02d}" for index in range(1, 33)]
        self.phase = 0.0
        self.timer = self.create_timer(1.0 / rate, self.publish)
        self.battery_sequence = 0
        self.battery_timer = self.create_timer(1.0 / battery_rate, self.publish_battery)

    def publish(self) -> None:
        stamp = self.get_clock().now().to_msg()
        phase = self.phase
        positions = [0.45 * math.sin(phase + index * 0.17) for index in range(32)]
        velocities = [0.45 * math.cos(phase + index * 0.17) for index in range(32)]
        efforts = [12.0 * math.sin(phase * 0.7 + index * 0.23) for index in range(32)]

        joints = JointState()
        joints.header.stamp = stamp
        joints.name = self.joint_names
        joints.position = positions
        joints.velocity = velocities
        joints.effort = efforts
        self.motor_publisher.publish(joints)

        self.phase += 0.08

    def publish_battery(self) -> None:
        battery = DiagnosticArray()
        battery.header.stamp = self.get_clock().now().to_msg()
        status = DiagnosticStatus()
        status.name = "batcan/simulation/summary"
        status.message = "BMS data received"
        status.hardware_id = "simulated-bms"
        values = {
            "voltage": 51.2 + 0.25 * math.sin(self.phase * 0.2),
            "current": -4.1 + 0.8 * math.sin(self.phase * 0.5),
            "temperature": 32.0 + 1.5 * math.sin(self.phase * 0.15),
            "percentage": 0.76 + 0.02 * math.sin(self.phase * 0.1),
            "power_supply_status": 2,
            "sequence": self.battery_sequence,
        }
        status.values = [KeyValue(key=key, value=str(value)) for key, value in values.items()]
        battery.status = [status]
        self.battery_publisher.publish(battery)
        self.battery_sequence += 1


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--motor-topic", default="/motor/q2w_upper_motor_joint_state")
    parser.add_argument("--battery-topic", default="/batcan/data")
    parser.add_argument("--rate", type=float, default=5.0)
    parser.add_argument("--battery-rate", type=float, default=5.0)
    args = parser.parse_args()

    rclpy.init()
    if args.rate <= 0 or args.battery_rate <= 0:
        parser.error("publish rates must be positive")
    node = BaizeSimulation(args.motor_topic, args.battery_topic, args.rate, args.battery_rate)
    try:
        rclpy.spin(node)
    except KeyboardInterrupt:
        pass
    finally:
        node.destroy_node()
        rclpy.shutdown()


if __name__ == "__main__":
    main()
