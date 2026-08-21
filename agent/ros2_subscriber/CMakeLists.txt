cmake_minimum_required(VERSION 3.16)
project(baize_ros2_subscriber VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Robot images use ROS 2 Humble. Keep the RPATH explicit so the helper also
# works when it is started directly by systemd instead of an interactive shell.
set(ROS_HUMBLE_LIB_DIRS
  /opt/ros/humble/lib
  /opt/ros/humble/lib/aarch64-linux-gnu
  /opt/ros/humble/lib/x86_64-linux-gnu)

find_package(ament_cmake REQUIRED)
find_package(diagnostic_msgs REQUIRED)
find_package(rclcpp REQUIRED)
find_package(sensor_msgs REQUIRED)

add_executable(baize-ros2-subscriber src/main.cpp)
ament_target_dependencies(baize-ros2-subscriber diagnostic_msgs rclcpp sensor_msgs)
target_compile_options(baize-ros2-subscriber PRIVATE -Wall -Wextra -Wpedantic -Werror)
set_target_properties(baize-ros2-subscriber PROPERTIES
  BUILD_RPATH "${ROS_HUMBLE_LIB_DIRS}"
  INSTALL_RPATH "${ROS_HUMBLE_LIB_DIRS}")
target_link_options(baize-ros2-subscriber PRIVATE -Wl,--disable-new-dtags)
target_compile_definitions(baize-ros2-subscriber PRIVATE
  BAIZE_ROS2_SUBSCRIBER_VERSION="${PROJECT_VERSION}")

install(TARGETS baize-ros2-subscriber RUNTIME DESTINATION lib/${PROJECT_NAME})
ament_package()
