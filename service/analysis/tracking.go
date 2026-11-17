package analysis

import (
	"fmt"
	"math"
	"sort"

	"health-bag-server/config"
	"health-bag-server/model"
)

type speedPoint struct {
	time  int64
	value float64
}

func tracking(commands, feedback []model.ActuatorFrame, cfg config.Tracking) ([]model.ActuatorTracking, error) {
	results := []model.ActuatorTracking{}
	if len(commands) == 0 && len(feedback) == 0 {
		return results, nil
	}
	if len(cfg.Channels) == 0 {
		return nil, fmt.Errorf("检测到执行器话题，但尚未配置电机字段及单位映射；请按协议填写 analysis.tracking.channels，原始bag已保留")
	}
	for _, channel := range cfg.Channels {
		cmd, err := mapped(commands, channel.Command, cfg.UseMessageStamp)
		if err != nil {
			return nil, fmt.Errorf("通道 %s 的指令数据解析失败：%w", channel.Name, err)
		}
		fb, err := mapped(feedback, channel.Feedback, cfg.UseMessageStamp)
		if err != nil {
			return nil, fmt.Errorf("通道 %s 的反馈数据解析失败：%w", channel.Name, err)
		}
		result := model.ActuatorTracking{Channel: channel.Name, Quantity: channel.Quantity, Status: "ok"}
		var cmdValues, fbValues, errors []float64
		for _, point := range cmd {
			cmdValues = append(cmdValues, point.value)
		}
		for _, point := range fb {
			fbValues = append(fbValues, point.value)
		}
		result.SpeedCmd = Stats(cmdValues)
		result.SpeedFeedback = Stats(fbValues)
		if len(cmd) == 0 {
			result.Status = "missing_command"
		}
		if len(fb) == 0 {
			result.Status = "missing_feedback"
		}
		cursor := -1
		for _, point := range fb {
			for cursor+1 < len(cmd) && cmd[cursor+1].time <= point.time {
				cursor++
			}
			if cursor < 0 || float64(point.time-cmd[cursor].time)/1e9 > cfg.MaxPairGap {
				result.UnmatchedCount++
				continue
			}
			errorValue := math.Abs(point.value - cmd[cursor].value)
			errors = append(errors, errorValue)
			if errorValue > channel.ErrorThreshold {
				result.SpeedExceedCount++
			}
		}
		result.SampleCount = int64(len(errors))
		result.SpeedError = Stats(errors)
		if len(cmd) > 0 && len(fb) > 0 && len(errors) == 0 {
			result.Status = "unmatched"
		} else if result.UnmatchedCount > 0 && result.Status == "ok" {
			result.Status = "partial"
		}
		var delays []float64
		for i := 1; i < len(cmd); i++ {
			delta := cmd[i].value - cmd[i-1].value
			if math.Abs(delta) < channel.StepThreshold {
				continue
			}
			deadline := cmd[i].time + int64(cfg.MaxResponseDelay*1e9)
			for j := i + 1; j < len(cmd); j++ {
				if math.Abs(cmd[j].value-cmd[i].value) >= channel.StepThreshold {
					if cmd[j].time < deadline {
						deadline = cmd[j].time
					}
					break
				}
			}
			target := cmd[i-1].value + delta*channel.ReachFraction
			begin := sort.Search(len(fb), func(j int) bool { return fb[j].time >= cmd[i].time })
			for j := begin; j < len(fb) && fb[j].time <= deadline; j++ {
				if delta > 0 && fb[j].value >= target || delta < 0 && fb[j].value <= target {
					delays = append(delays, float64(fb[j].time-cmd[i].time)/1e9)
					break
				}
			}
		}
		if len(delays) > 0 {
			var sum float64
			for _, delay := range delays {
				sum += delay
			}
			result.ResponseDelay = model.Float(sum / float64(len(delays)))
		}
		results = append(results, result)
	}
	return results, nil
}

func mapped(frames []model.ActuatorFrame, fields []config.Field, useSource bool) ([]speedPoint, error) {
	points := make([]speedPoint, 0, len(frames))
	for _, frame := range frames {
		var value float64
		for _, field := range fields {
			if field.ActuatorIndex >= len(frame.Actuators) {
				return nil, fmt.Errorf("执行器索引 %d 不存在", field.ActuatorIndex)
			}
			a := frame.Actuators[field.ActuatorIndex]
			if field.ActuatorType != nil && a.Type != *field.ActuatorType {
				return nil, fmt.Errorf("执行器类型与配置不一致：%d", a.Type)
			}
			if field.MotorIndex >= len(a.Motors) {
				return nil, fmt.Errorf("电机索引 %d 不存在", field.MotorIndex)
			}
			motor := a.Motors[field.MotorIndex]
			if field.MotorType != nil && motor.Type != *field.MotorType {
				return nil, fmt.Errorf("电机类型与配置不一致：%d", motor.Type)
			}
			if field.DataIndex >= len(motor.Data) {
				return nil, fmt.Errorf("电机数据索引 %d 不存在", field.DataIndex)
			}
			value += motor.Data[field.DataIndex] * field.Scale
		}
		if !model.Finite(value) {
			return nil, fmt.Errorf("单位换算后的速度数值无效")
		}
		stamp := frame.BagNS
		if useSource {
			var err error
			stamp, err = model.SecondsToNS(frame.Stamp)
			if err != nil {
				return nil, err
			}
		}
		points = append(points, speedPoint{stamp, value})
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].time < points[j].time })
	return points, nil
}
