#include <cmath>
#include <cctype>
#include <chrono>
#include <csignal>
#include <cstdint>
#include <cstdio>
#include <iostream>
#include <limits>
#include <mutex>
#include <stdexcept>
#include <string>
#include <utility>

#include <rclcpp/rclcpp.hpp>

#ifndef BAIZE_ROS2_SUBSCRIBER_VERSION
#define BAIZE_ROS2_SUBSCRIBER_VERSION "dev"
#endif

namespace {

std::mutex output_mutex;

std::uint64_t wallClockNS() {
  return static_cast<std::uint64_t>(std::chrono::duration_cast<std::chrono::nanoseconds>(
      std::chrono::system_clock::now().time_since_epoch()).count());
}

void writeAll(const void *data, std::size_t size) {
  const auto *bytes = static_cast<const std::uint8_t *>(data);
  while (size > 0) {
    const auto written = std::fwrite(bytes, 1, size, stdout);
    if (written == 0) throw std::runtime_error("cannot write ROS2 subscriber output");
    bytes += written;
    size -= written;
  }
}

void writeU64(std::uint64_t value) {
  std::uint8_t bytes[8];
  for (int index = 0; index < 8; ++index) {
    bytes[index] = static_cast<std::uint8_t>((value >> (index * 8)) & 0xff);
  }
  writeAll(bytes, sizeof(bytes));
}

void writeU32(std::uint32_t value) {
  const std::uint8_t bytes[] = {
      static_cast<std::uint8_t>(value & 0xff),
      static_cast<std::uint8_t>((value >> 8) & 0xff),
      static_cast<std::uint8_t>((value >> 16) & 0xff),
      static_cast<std::uint8_t>((value >> 24) & 0xff)};
  writeAll(bytes, sizeof(bytes));
}

void emitRaw(const std::shared_ptr<rclcpp::SerializedMessage> &message) {
  const auto &serialized = message->get_rcl_serialized_message();
  if (serialized.buffer == nullptr || serialized.buffer_length == 0 ||
      serialized.buffer_length > std::numeric_limits<std::uint32_t>::max()) {
    return;
  }
  const std::uint8_t header[] = {'B', 'Z', 'R', '1', 1};
  std::lock_guard<std::mutex> lock(output_mutex);
  writeAll(header, sizeof(header));
  writeU64(wallClockNS());
  writeU32(static_cast<std::uint32_t>(serialized.buffer_length));
  writeAll(serialized.buffer, serialized.buffer_length);
  std::fflush(stdout);
}

class Subscriber final : public rclcpp::Node {
 public:
  Subscriber(const std::string &topic, const std::string &message_type, std::chrono::milliseconds read_timeout)
      : Node("baize_agent_subscriber"), last_message_(this->get_clock()->now()), read_timeout_(read_timeout) {
    const auto qos = message_type == "sensor_msgs/msg/JointState"
                         ? rclcpp::SensorDataQoS().keep_last(256)
                         : rclcpp::QoS(100);
    subscription_ = create_generic_subscription(
        topic, message_type, qos,
        [this](std::shared_ptr<rclcpp::SerializedMessage> message) {
          last_message_ = this->get_clock()->now();
          emitRaw(message);
        });
    watchdog_ = create_wall_timer(std::chrono::seconds(1), [this]() {
      if ((this->get_clock()->now() - last_message_).seconds() >=
          std::chrono::duration<double>(read_timeout_).count()) {
        RCLCPP_WARN(this->get_logger(), "no messages received for %lldms; restarting subscription",
                    static_cast<long long>(read_timeout_.count()));
        rclcpp::shutdown();
      }
    });
  }

 private:
  std::shared_ptr<rclcpp::GenericSubscription> subscription_;
  rclcpp::TimerBase::SharedPtr watchdog_;
  rclcpp::Time last_message_;
  std::chrono::milliseconds read_timeout_;
};

std::chrono::milliseconds parseReadTimeout(const std::string &value) {
  if (value.size() < 3) throw std::invalid_argument("read-timeout must include a duration suffix");
  std::size_t suffix_start = value.size();
  if (value.size() >= 2 && value.substr(value.size() - 2) == "ms") suffix_start -= 2;
  else if (std::isalpha(static_cast<unsigned char>(value.back()))) suffix_start -= 1;
  else throw std::invalid_argument("read-timeout must include a duration suffix");
  if (suffix_start == 0) throw std::invalid_argument("read-timeout must start with a number");
  const auto number = std::stod(value.substr(0, suffix_start));
  if (!std::isfinite(number) || number <= 0) throw std::invalid_argument("read-timeout must be positive");
  const auto suffix = value.substr(suffix_start);
  double multiplier = 0;
  if (suffix == "ms") multiplier = 1;
  else if (suffix == "s") multiplier = 1000;
  else if (suffix == "m") multiplier = 60 * 1000;
  else if (suffix == "h") multiplier = 60 * 60 * 1000;
  else throw std::invalid_argument("read-timeout suffix must be ms, s, m, or h");
  const auto milliseconds = number * multiplier;
  if (milliseconds < 1 || milliseconds > static_cast<double>(std::numeric_limits<long long>::max())) {
    throw std::invalid_argument("read-timeout is out of range");
  }
  return std::chrono::milliseconds(static_cast<long long>(milliseconds));
}

void usage(std::ostream &output) {
  output << "Usage: baize-ros2-subscriber --topic TOPIC --message-type TYPE\n"
         << "       --read-timeout DURATION (for example 5s)\n"
         << "       baize-ros2-subscriber --version\n";
}

}  // namespace

int main(int argc, char **argv) {
  std::signal(SIGPIPE, SIG_IGN);
  try {
    std::string topic;
    std::string message_type;
    std::chrono::milliseconds read_timeout(5000);
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
      } else if (argument == "--read-timeout" && index + 1 < argc) {
        read_timeout = parseReadTimeout(argv[++index]);
      } else {
        throw std::invalid_argument("unknown or incomplete option: " + argument);
      }
    }
    if (topic.empty() || message_type.empty()) {
      usage(std::cerr);
      return 2;
    }
    rclcpp::init(0, nullptr);
    auto node = std::make_shared<Subscriber>(topic, message_type, read_timeout);
    rclcpp::spin(node);
    rclcpp::shutdown();
    return 0;
  } catch (const std::exception &error) {
    if (rclcpp::ok()) rclcpp::shutdown();
    std::cerr << "baize-ros2-subscriber: " << error.what() << '\n';
    return 1;
  }
}
