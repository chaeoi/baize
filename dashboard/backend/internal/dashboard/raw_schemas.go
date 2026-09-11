package dashboard

import "baize/shared/rawstream"

const rosHeaderSchema = `
================================================================================
MSG: std_msgs/Header
builtin_interfaces/Time stamp
string frame_id
================================================================================
MSG: builtin_interfaces/Time
int32 sec
uint32 nanosec
`

func schemaData(record rawstream.Record) ([]byte, string) {
	if record.Kind == rawstream.KindHost {
		return []byte(`{"type":"object","properties":{"system":{"type":"object"},"gpus":{"type":"array","items":{"type":"object"}},"errors":{"type":"array","items":{"type":"object"}}}}`), "jsonschema"
	}
	switch record.MessageType {
	case "sensor_msgs/msg/JointState":
		return []byte("std_msgs/Header header\nstring[] name\nfloat64[] position\nfloat64[] velocity\nfloat64[] effort\n" + rosHeaderSchema), "ros2msg"
	case "diagnostic_msgs/msg/DiagnosticArray":
		return []byte("std_msgs/Header header\ndiagnostic_msgs/DiagnosticStatus[] status\n" + rosHeaderSchema + `
================================================================================
MSG: diagnostic_msgs/DiagnosticStatus
byte OK=0
byte WARN=1
byte ERROR=2
byte STALE=3
byte level
string name
string message
string hardware_id
diagnostic_msgs/KeyValue[] values
================================================================================
MSG: diagnostic_msgs/KeyValue
string key
string value
`), "ros2msg"
	default:
		return nil, ""
	}
}
