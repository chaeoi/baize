package model

import (
	"math"
	"reflect"
)

// SanitizeFinite replaces non-finite numeric values before a telemetry report
// crosses the JSON boundary. ROS2 standard messages use NaN for unavailable
// fields, while encoding/json intentionally rejects NaN and infinity.
func SanitizeFinite(telemetry *Telemetry) int {
	if telemetry == nil {
		return 0
	}
	return sanitizeValue(reflect.ValueOf(telemetry))
}

func sanitizeValue(value reflect.Value) int {
	if !value.IsValid() {
		return 0
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return 0
		}
		return sanitizeValue(value.Elem())
	case reflect.Struct:
		count := 0
		for index := 0; index < value.NumField(); index++ {
			count += sanitizeValue(value.Field(index))
		}
		return count
	case reflect.Slice, reflect.Array:
		count := 0
		for index := 0; index < value.Len(); index++ {
			count += sanitizeValue(value.Index(index))
		}
		return count
	case reflect.Float32, reflect.Float64:
		if !value.CanSet() || !math.IsNaN(value.Float()) && !math.IsInf(value.Float(), 0) {
			return 0
		}
		value.SetFloat(0)
		return 1
	default:
		return 0
	}
}
