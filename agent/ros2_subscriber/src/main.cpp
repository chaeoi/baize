#include <csignal>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <iomanip>
#include <iostream>
#include <limits>
#include <mutex>
#include <sstream>
#include <stdexcept>
#include <string>
#include <string_view>
#include <vector>
#include <chrono>

#include <diagnostic_msgs/msg/diagnostic_array.hpp>
#include <rclcpp/rclcpp.hpp>
#include <sensor_msgs/msg/joint_state.hpp>

#ifndef BAIZE_ROS2_SUBSCRIBER_VERSION
#define BAIZE_ROS2_SUBSCRIBER_VERSION "dev"
#endif

namespace {

std::mutex output_mutex;
std::vector<std::string> last_names;

std::int64_t wallClockNS() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

std::int64_t messageStampNS(const builtin_interfaces::msg::Time &stamp) {
  if (stamp.sec == 0 && stamp.nanosec == 0) {
    return wallClockNS();
  }
  return static_cast<std::int64_t>(stamp.sec) * 1000000000LL +
         static_cast<std::int64_t>(stamp.nanosec);
}

void writeAll(const void *data, std::size_t size) {
  const auto *bytes = static_cast<const std::uint8_t *>(data);
  while (size > 0) {
    const auto written = std::fwrite(bytes, 1, size, stdout);
    if (written == 0) {
      throw std::runtime_error("cannot write ROS2 subscriber output");
    }
    bytes += written;
    size -= written;
  }
}

void writeU16(std::uint16_t value) {
  const std::uint8_t bytes[] = {
      static_cast<std::uint8_t>(value & 0xff),
      static_cast<std::uint8_t>((value >> 8) & 0xff)};
  writeAll(bytes, sizeof(bytes));
}

void writeU64(std::uint64_t value) {
  std::uint8_t bytes[8];
  for (int index = 0; index < 8; ++index) {
    bytes[index] = static_cast<std::uint8_t>((value >> (index * 8)) & 0xff);
  }
  writeAll(bytes, sizeof(bytes));
}

void emitMotor(const sensor_msgs::msg::JointState &message) {
  const auto count = message.name.size();
  if (count == 0 || count > std::numeric_limits<std::uint16_t>::max() ||
      count > 1024 || message.position.size() != count ||
      message.velocity.size() != count || message.effort.size() != count) {
    return;
  }

  bool include_names = message.name.size() != last_names.size();
  for (std::size_t index = 0; !include_names && index < count; ++index) {
    if (message.name[index] != last_names[index]) {
      include_names = true;
    }
  }
  for (const auto &name : message.name) {
    if (name.size() > std::numeric_limits<std::uint16_t>::max()) {
      return;
    }
  }
  const auto stamp = messageStampNS(message.header.stamp);
  const std::uint8_t header[] = {'B', 'Z', 'M', '1', 1,
                                 static_cast<std::uint8_t>(include_names ? 1 : 0)};

  std::lock_guard<std::mutex> lock(output_mutex);
  writeAll(header, sizeof(header));
  writeU64(static_cast<std::uint64_t>(stamp));
  writeU16(static_cast<std::uint16_t>(count));
  if (include_names) {
    for (const auto &name : message.name) {
      writeU16(static_cast<std::uint16_t>(name.size()));
      writeAll(name.data(), name.size());
    }
    last_names = message.name;
  }
  for (const auto *values : {&message.position, &message.velocity,
                             &message.effort}) {
    writeAll(values->data(), count * sizeof(double));
  }
  std::fflush(stdout);
}

std::string jsonEscape(std::string_view value) {
  std::ostringstream output;
  for (const auto character : value) {
    switch (character) {
      case '"': output << "\\\""; break;
      case '\\': output << "\\\\"; break;
      case '\b': output << "\\b"; break;
      case '\f': output << "\\f"; break;
      case '\n': output << "\\n"; break;
      case '\r': output << "\\r"; break;
      case '\t': output << "\\t"; break;
      default:
        if (static_cast<unsigned char>(character) < 0x20) {
          output << "\\u" << std::hex << std::setw(4) << std::setfill('0')
                 << static_cast<int>(static_cast<unsigned char>(character));
        } else {
          output << character;
        }
    }
  }
  return output.str();
}

void emitBMS(const diagnostic_msgs::msg::DiagnosticArray &message) {
  std::ostringstream output;
  output << "{\"type\":\"bms\",\"stamp_ns\":"
         << messageStampNS(message.header.stamp)
         << ",\"status\":[";
  for (std::size_t status_index = 0; status_index < message.status.size();
       ++status_index) {
    if (status_index != 0) output << ',';
    const auto &status = message.status[status_index];
    output << "{\"name\":\"" << jsonEscape(status.name)
           << "\",\"message\":\"" << jsonEscape(status.message)
           << "\",\"hardware_id\":\"" << jsonEscape(status.hardware_id)
           << "\",\"values\":[";
    for (std::size_t value_index = 0; value_index < status.values.size();
         ++value_index) {
      if (value_index != 0) output << ',';
      const auto &value = status.values[value_index];
      output << "{\"key\":\"" << jsonEscape(value.key)
             << "\",\"value\":\"" << jsonEscape(value.value) << "\"}";
    }
    output << "]}";
  }
  output << "]}\n";
  const auto text = output.str();
  std::lock_guard<std::mutex> lock(output_mutex);
  writeAll(text.data(), text.size());
  std::fflush(stdout);
}

class Subscriber final : public rclcpp::Node {
 public:
  Subscriber(std::string topic, std::string message_type)
      : Node("baize_agent_subscriber") {
    if (message_type == "sensor_msgs/msg/JointState") {
      motor_subscription_ = create_subscription<sensor_msgs::msg::JointState>(
          topic, rclcpp::SensorDataQoS(),
          [](sensor_msgs::msg::JointState::ConstSharedPtr message) {
            emitMotor(*message);
          });
      return;
    }
    if (message_type == "diagnostic_msgs/msg/DiagnosticArray") {
      bms_subscription_ =
          create_subscription<diagnostic_msgs::msg::DiagnosticArray>(
              topic, rclcpp::QoS(10),
              [](diagnostic_msgs::msg::DiagnosticArray::ConstSharedPtr message) {
                emitBMS(*message);
              });
      return;
    }
    throw std::invalid_argument("unsupported ROS2 message type: " + message_type);
  }

 private:
  rclcpp::Subscription<sensor_msgs::msg::JointState>::SharedPtr
      motor_subscription_;
  rclcpp::Subscription<diagnostic_msgs::msg::DiagnosticArray>::SharedPtr
      bms_subscription_;
};

void usage(std::ostream &output) {
  output << "Usage: baize-ros2-subscriber --topic TOPIC --message-type TYPE\n"
         << "       baize-ros2-subscriber --version\n";
}

}  // namespace

int main(int argc, char **argv) {
  std::signal(SIGPIPE, SIG_IGN);
  try {
    std::string topic;
    std::string message_type;
    for (int index = 1; index < argc; ++index) {
      const std::string argument = argv[index];
      if (argument == "--version") {
        std::cout << BAIZE_ROS2_SUBSCRIBER_VERSION << '\n';
        return 0;
      }
      if (argument == "--help" || argument == "-h") {
        usage(std::cout);
        return 0;
      }
      if (argument == "--topic" && index + 1 < argc) {
        topic = argv[++index];
      } else if (argument == "--message-type" && index + 1 < argc) {
        message_type = argv[++index];
      } else {
        throw std::invalid_argument("unknown or incomplete option: " + argument);
      }
    }
    if (topic.empty() || message_type.empty()) {
      usage(std::cerr);
      return 2;
    }
    // The command-line options have already been consumed by this helper.
    // Do not pass them to rclcpp, which would otherwise try to parse
    // --topic/--message-type as ROS arguments.
    rclcpp::init(0, nullptr);
    auto node = std::make_shared<Subscriber>(topic, message_type);
    rclcpp::spin(node);
    rclcpp::shutdown();
    return 0;
  } catch (const std::exception &error) {
    if (rclcpp::ok()) rclcpp::shutdown();
    std::cerr << "baize-ros2-subscriber: " << error.what() << '\n';
    return 1;
  }
}
