//go:build !linux

package collector

import (
	"context"
	"errors"

	"baize/agent/internal/config"
	"baize/shared/model"
)

func collectMotorCAN(context.Context, config.MotorConfig) (model.MotorSnapshot, error) {
	return model.MotorSnapshot{Enabled: true, Source: "can_query"}, errors.New("CAN motor collection is only supported on linux")
}
