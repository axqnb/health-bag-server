package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"health-bag-server/config"
	"health-bag-server/model"
)

func Stats(values []float64) model.Stats {
	if len(values) == 0 {
		return model.Stats{}
	}
	copyValues := make([]float64, len(values))
	var total float64
	for i, value := range values {
		value = math.Abs(value)
		copyValues[i] = value
		total += value
	}
	sort.Float64s(copyValues)
	position := 0.95 * float64(len(copyValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	p95 := copyValues[lower] + (copyValues[upper]-copyValues[lower])*(position-float64(lower))
	return model.Stats{Mean: model.Float(total / float64(len(values))), Max: model.Float(copyValues[len(values)-1]), P95: model.Float(p95)}
}

func Build(ctx context.Context, data *model.Data, input config.Input, cfg config.Analysis) (*model.Bundle, error) {
	m := data.Mission
	if m.EndNS <= m.StartNS || input.RobotID <= 0 {
		return nil, fmt.Errorf("任务标识或起止时间无效")
	}
	b := &model.Bundle{Task: model.Task{TaskKey: m.Key(input.RobotID), RobotID: input.RobotID,
		MapName: input.MapName, IsLoading: input.IsLoading, StartID: m.StartID, FinishID: m.FinishID,
		StartTimestamp: model.FormatSeconds(m.StartNS), EndTimestamp: model.FormatSeconds(m.EndNS), AnalysisVersion: model.AnalysisVersion},
		Reach: append([]model.ReachPrecision{}, data.Reach...), Servo: append([]model.ServoResult{}, data.Servo...)}
	warnings := append([]string{}, data.Warnings...)
	if input.MapName == nil {
		warnings = append(warnings, "map_name_unknown")
	}
	if input.IsLoading == nil {
		warnings = append(warnings, "is_loading_unknown")
	}
	within := func(stamp int64) bool { return stamp >= m.StartNS && stamp <= m.EndNS }
	controls := make([]model.ControlSample, 0, len(data.Control))
	for _, sample := range data.Control {
		if within(sample.BagNS) {
			controls = append(controls, sample)
		} else {
			return nil, fmt.Errorf("过程控制采样时间超出了任务起止范围")
		}
	}
	odometry := make([]model.OdomSample, 0, len(data.Odom))
	for _, sample := range data.Odom {
		if within(sample.BagNS) {
			odometry = append(odometry, sample)
		} else {
			return nil, fmt.Errorf("里程计采样时间超出了任务起止范围")
		}
	}
	for _, frames := range [][]model.ActuatorFrame{data.Commands, data.Feedback} {
		for _, frame := range frames {
			if !within(frame.BagNS) {
				return nil, fmt.Errorf("执行器采样时间超出了任务起止范围")
			}
		}
	}
	var resets int
	b.Process, resets = processControl(controls, cfg)
	if resets > 0 {
		warnings = append(warnings, "localization_counter_reset_within_task")
	}
	var err error
	b.Twisting, err = twisting(ctx, odometry, cfg.Twisting)
	if err != nil {
		return nil, err
	}
	b.Tracking, err = tracking(data.Commands, data.Feedback, cfg.Tracking)
	if err != nil {
		return nil, err
	}
	if len(b.Reach) > 0 {
		warnings = append(warnings, "reach_values_follow_vehicle_learning_semantics_not_verified_residuals")
	}
	for i := range b.Reach {
		b.Reach[i].EventKey = model.Hash(b.Task.TaskKey, b.Reach[i].EventKey)
	}
	for i := range b.Servo {
		s := &b.Servo[i]
		s.EventKey = model.Hash(b.Task.TaskKey, s.EventKey)
		if !within(s.TimestampNS) {
			warnings = append(warnings, "servo_event_outside_navigation_window")
		}
		if cfg.ServoExpected == nil {
			warnings = append(warnings, "servo_error_requires_expected_pose")
			continue
		}
		norm := s.QX*s.QX + s.QY*s.QY + s.QZ*s.QZ + s.QW*s.QW
		if !model.Finite(norm) || norm < 1e-12 {
			warnings = append(warnings, "servo_orientation_invalid")
			continue
		}
		x, y, z, w := s.QX/math.Sqrt(norm), s.QY/math.Sqrt(norm), s.QZ/math.Sqrt(norm), s.QW/math.Sqrt(norm)
		yaw := math.Atan2(2*(w*z+x*y), 1-2*(y*y+z*z))
		expected := cfg.ServoExpected
		dx, dy := s.X-expected.X, s.Y-expected.Y
		c, sn := math.Cos(expected.Yaw), math.Sin(expected.Yaw)
		s.ErrorX = model.Float(c*dx + sn*dy)
		s.ErrorY = model.Float(-sn*dx + c*dy)
		s.ErrorZ = model.Float(s.Z - expected.Z)
		s.ErrorYaw = model.Float(model.NormalizeAngle(yaw - expected.Yaw))
	}
	sort.Slice(b.Reach, func(i, j int) bool { return b.Reach[i].EventKey < b.Reach[j].EventKey })
	sort.Slice(b.Servo, func(i, j int) bool { return b.Servo[i].EventKey < b.Servo[j].EventKey })
	sort.Strings(warnings)
	unique := []string{}
	for _, warning := range warnings {
		if len(unique) == 0 || unique[len(unique)-1] != warning {
			unique = append(unique, warning)
		}
	}
	quality, err := json.Marshal(struct {
		TopicCounts map[string]uint64 `json:"topic_counts"`
		Warnings    []string          `json:"warnings"`
		Config      config.Analysis   `json:"analysis_config"`
	}{data.TopicCounts, unique, cfg})
	if err != nil {
		return nil, err
	}
	b.Task.Quality = string(quality)
	digest, err := b.Digest()
	if err != nil {
		return nil, err
	}
	b.Task.DataDigest = digest
	return b, nil
}

func processControl(samples []model.ControlSample, cfg config.Analysis) (model.ProcessControl, int) {
	result := model.ProcessControl{Status: "missing", SampleCount: int64(len(samples))}
	if len(samples) == 0 {
		return result, 0
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].BagNS < samples[j].BagNS })
	ys, yaws := make([]float64, 0, len(samples)), make([]float64, 0, len(samples))
	var previous uint32
	var jumps int64
	resets := 0
	for _, sample := range samples {
		ys = append(ys, sample.Y)
		yaws = append(yaws, sample.Yaw)
		if math.Abs(sample.Y) > cfg.LateralThreshold {
			result.LateralExceedCount++
		}
		if math.Abs(sample.Yaw) > cfg.HeadingThreshold*math.Pi/180 {
			result.HeadingExceedCount++
		}
		if sample.JumpCount >= previous {
			jumps += int64(sample.JumpCount - previous)
		} else {
			jumps += int64(sample.JumpCount)
			resets++
		}
		previous = sample.JumpCount
	}
	result.Status = "ok"
	result.JumpCount = &jumps
	result.LinearError = Stats(ys)
	result.AngularError = Stats(yaws)
	return result, resets
}
