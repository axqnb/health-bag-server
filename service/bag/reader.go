package bag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	rosbag "github.com/foxglove/go-rosbag"
	"github.com/foxglove/go-rosbag/ros1msg"
	"health-bag-server/model"
)

type Reader struct {
	MaxBytes    int64
	MaxMessages uint64
}

// Read requires a finalized indexed bag. A partial FTP upload is never repaired
// or treated as a successfully imported task.
func (r Reader) Read(ctx context.Context, path string) (data *model.Data, err error) {
	defer func() {
		if value := recover(); value != nil {
			data = nil
			err = fmt.Errorf("bag文件或消息格式异常：%v", value)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() > r.MaxBytes {
		return nil, fmt.Errorf("bag文件超过程序支持的大小上限")
	}
	if err := validateFile(f, stat.Size()); err != nil {
		return nil, err
	}
	reader, err := rosbag.NewReader(f)
	if err != nil {
		return nil, err
	}
	op, record, err := rosbag.ReadRecord(f)
	if err != nil || op != rosbag.OpBagHeader {
		return nil, fmt.Errorf("bag文件头无效：%v", err)
	}
	header, err := rosbag.ParseBagHeader(record)
	if err != nil {
		return nil, err
	}
	if header.IndexPos == 0 || header.IndexPos >= uint64(stat.Size()) {
		return nil, fmt.Errorf("bag尚未生成索引，或索引数据尚未上传完成")
	}
	info, err := reader.Info()
	if err != nil {
		return nil, err
	}
	if len(info.ChunkInfos) != int(header.ChunkCount) || len(info.Connections) != int(header.ConnCount) {
		return nil, fmt.Errorf("bag索引数据不完整")
	}
	if info.MessageCount == 0 || info.MessageCount > r.MaxMessages {
		return nil, fmt.Errorf("bag消息数量无效或超过上限：%d", info.MessageCount)
	}
	it, err := reader.Messages()
	if err != nil {
		return nil, err
	}
	data = &model.Data{TopicCounts: map[string]uint64{}}
	schemas := map[uint32][]ros1msg.Field{}
	actualCounts := map[uint32]int64{}
	var mission *model.Mission
	seenEvents := map[string]bool{}
	for it.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, message, err := it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if conn == nil || message == nil {
			return nil, fmt.Errorf("bag话题连接或消息无效")
		}
		// 容器计数包含所有消息，业务质量统计只包含支持的话题。
		actualCounts[message.Conn]++
		if !knownTopic(conn.Topic) {
			continue
		}
		if message.Time > math.MaxInt64 {
			return nil, fmt.Errorf("bag消息时间无效")
		}
		data.TopicCounts[conn.Topic]++
		fields := schemas[conn.Conn]
		if fields == nil {
			pkg, _, ok := strings.Cut(conn.Data.Type, "/")
			if !ok {
				return nil, fmt.Errorf("消息类型格式无效：%s", conn.Data.Type)
			}
			fields, err = ros1msg.ParseMessageDefinition(pkg, conn.Data.MessageDefinition)
			if err != nil {
				return nil, fmt.Errorf("解析消息类型 %s 的定义失败：%w", conn.Data.Type, err)
			}
			schemas[conn.Conn] = fields
		}
		output, nonFinite, err := decodeMessage(fields, message.Data)
		if err != nil {
			return nil, fmt.Errorf("解码话题 %s 的消息失败：%w", conn.Topic, err)
		}
		if nonFinite {
			data.Warnings = append(data.Warnings, "non_finite_ros_fields")
		}
		t := int64(message.Time)
		switch conn.Topic {
		case model.MissionTopic, model.ReachTopic, model.ControlTopic:
			if conn.Data.Type != "jzstd_msgs/DataCollection" {
				return nil, fmt.Errorf("话题 %s 的消息类型不符合要求：%s", conn.Topic, conn.Data.Type)
			}
			var envelope struct {
				Timestamp json.Number `json:"timestamp"`
				Type      string      `json:"type"`
				Version   string      `json:"version"`
				Data      string      `json:"data"`
			}
			if err := json.Unmarshal(output, &envelope); err != nil {
				return nil, err
			}
			expected := strings.TrimPrefix(conn.Topic, "/nav_manager/")
			if envelope.Type != "" && envelope.Type != expected {
				return nil, fmt.Errorf("DataCollection.type 与话题 %s 不匹配", conn.Topic)
			}
			if envelope.Version != "" && envelope.Version != "1.0" && envelope.Version != "1.0.0" {
				return nil, fmt.Errorf("暂不支持DataCollection版本 %q", envelope.Version)
			}
			body := []byte(envelope.Data)
			if !json.Valid(body) {
				return nil, fmt.Errorf("话题 %s 的JSON数据无效", conn.Topic)
			}
			switch conn.Topic {
			case model.MissionTopic:
				m, err := parseMission(body)
				if err != nil {
					return nil, err
				}
				if mission != nil && mission.Key(0) != m.Key(0) {
					return nil, fmt.Errorf("同一个bag包含多个不同任务，无法按单任务入库")
				}
				mission = &m
			case model.ControlTopic:
				sample, err := parseControl(body, t)
				if err != nil {
					return nil, err
				}
				data.Control = append(data.Control, sample)
			case model.ReachTopic:
				event, err := parseReach(body, t)
				if err != nil {
					return nil, err
				}
				if !seenEvents[event.EventKey] {
					data.Reach = append(data.Reach, event)
					seenEvents[event.EventKey] = true
				}
			}
		case model.OdomTopic:
			if conn.Data.Type != "nav_msgs/Odometry" {
				return nil, fmt.Errorf("话题 %s 必须使用 nav_msgs/Odometry 类型", conn.Topic)
			}
			sample, err := parseOdom(output, t)
			if err != nil {
				return nil, err
			}
			data.Odom = append(data.Odom, sample)
		case model.CommandTopic, model.FeedbackTopic:
			var frame model.ActuatorFrame
			if err := required(output, "stamp", "actuators"); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(output, &frame); err != nil {
				return nil, err
			}
			if stamp, err := frame.Stamp.Float64(); err != nil || !model.Finite(stamp) {
				return nil, fmt.Errorf("执行器消息的时间戳无效")
			}
			for _, actuator := range frame.Actuators {
				for _, motor := range actuator.Motors {
					if !model.Finite(motor.Data...) {
						return nil, fmt.Errorf("电机反馈数据包含无效数值")
					}
				}
			}
			frame.BagNS = t
			if conn.Topic == model.CommandTopic {
				data.Commands = append(data.Commands, frame)
			} else {
				data.Feedback = append(data.Feedback, frame)
			}
		case model.ServoTopic:
			if conn.Data.Type != "visual_servo_msgs/IbvsConstrainedActionResult" {
				return nil, fmt.Errorf("伺服结果消息类型不符合要求：%s", conn.Data.Type)
			}
			event, err := parseServo(output, t)
			if err != nil {
				return nil, err
			}
			if !seenEvents[event.EventKey] {
				data.Servo = append(data.Servo, event)
				seenEvents[event.EventKey] = true
			}
		}
	}
	for conn, count := range info.ConnectionMessageCounts() {
		if actualCounts[conn] != count {
			return nil, fmt.Errorf("话题连接 %d 的实际消息数与索引不一致", conn)
		}
	}
	var total uint64
	for _, count := range actualCounts {
		total += uint64(count)
	}
	if total != info.MessageCount {
		return nil, fmt.Errorf("bag消息数据不完整")
	}
	if mission == nil {
		return nil, fmt.Errorf("缺少任务信息话题 %s，无法仅根据文件名确定任务", model.MissionTopic)
	}
	data.Mission = *mission
	return data, nil
}

func knownTopic(topic string) bool {
	switch topic {
	case model.MissionTopic, model.ReachTopic, model.ControlTopic, model.OdomTopic, model.CommandTopic, model.FeedbackTopic, model.ServoTopic:
		return true
	}
	return false
}

func required(raw []byte, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("缺少必要字段 %s", field)
		}
	}
	return nil
}

func parseMission(raw []byte) (model.Mission, error) {
	var m model.Mission
	if err := required(raw, "start_id", "finish_id", "start_timestamp", "end_timestamp"); err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	var err error
	if m.StartNS, err = model.SecondsToNS(m.Start); err != nil {
		return m, err
	}
	if m.EndNS, err = model.SecondsToNS(m.End); err != nil {
		return m, err
	}
	if m.EndNS <= m.StartNS {
		return m, fmt.Errorf("任务结束时间必须晚于开始时间")
	}
	return m, nil
}

func stampOrBag(stamp json.Number, fallback int64) (int64, error) {
	if stamp == "" || stamp == "0" || stamp == "0.000000000" {
		return fallback, nil
	}
	return model.SecondsToNS(stamp)
}

func parseControl(raw []byte, bagNS int64) (model.ControlSample, error) {
	var value struct {
		Y         float64     `json:"control_error_y"`
		Yaw       float64     `json:"control_error_yaw"`
		Jumps     uint32      `json:"loc_jump_times"`
		Timestamp json.Number `json:"timestamp"`
	}
	if err := required(raw, "control_error_y", "control_error_yaw", "loc_jump_times", "timestamp"); err != nil {
		return model.ControlSample{}, err
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.ControlSample{}, err
	}
	if !model.Finite(value.Y, value.Yaw) {
		return model.ControlSample{}, fmt.Errorf("过程控制误差数据无效")
	}
	stamp, err := stampOrBag(value.Timestamp, bagNS)
	return model.ControlSample{BagNS: bagNS, SourceNS: stamp, Y: value.Y, Yaw: model.NormalizeAngle(value.Yaw), JumpCount: value.Jumps}, err
}

func parseReach(raw []byte, bagNS int64) (model.ReachPrecision, error) {
	var value struct {
		Control   json.RawMessage `json:"control_error"`
		Closed    json.RawMessage `json:"Closedloop_error"`
		Locate    json.RawMessage `json:"locate_error"`
		Type      int             `json:"compensation_type"`
		Timestamp json.Number     `json:"timestamp"`
	}
	result := model.ReachPrecision{}
	if err := required(raw, "control_error", "Closedloop_error", "locate_error", "compensation_type", "timestamp"); err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return result, err
	}
	for _, item := range []struct {
		raw  []byte
		pose *model.PoseError
	}{{value.Control, &result.Control}, {value.Closed, &result.ClosedLoop}, {value.Locate, &result.Locate}} {
		if err := required(item.raw, "x", "y", "yaw"); err != nil {
			return result, err
		}
		if err := json.Unmarshal(item.raw, item.pose); err != nil {
			return result, err
		}
		if !model.Finite(item.pose.X, item.pose.Y, item.pose.Yaw) {
			return result, fmt.Errorf("导航到点精度数据无效")
		}
	}
	stamp, err := stampOrBag(value.Timestamp, bagNS)
	if err != nil {
		return result, err
	}
	canonical, err := model.CanonicalJSON(raw)
	if err != nil {
		return result, err
	}
	result.Event = model.Event{TimestampNS: stamp, EventKey: model.Hash("reach", fmt.Sprint(stamp), canonical)}
	result.RawData = canonical
	result.CompensationType = value.Type
	return result, nil
}

func parseOdom(raw []byte, bagNS int64) (model.OdomSample, error) {
	var value struct {
		Header struct {
			Stamp json.Number `json:"stamp"`
		} `json:"header"`
		Twist struct {
			Twist struct {
				Linear struct {
					X *float64 `json:"x"`
				} `json:"linear"`
				Angular struct {
					Z *float64 `json:"z"`
				} `json:"angular"`
			} `json:"twist"`
		} `json:"twist"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.OdomSample{}, err
	}
	x, z := value.Twist.Twist.Linear.X, value.Twist.Twist.Angular.Z
	if x == nil || z == nil || !model.Finite(*x, *z) {
		return model.OdomSample{}, fmt.Errorf("里程计的线速度或角速度数据无效")
	}
	stamp, err := stampOrBag(value.Header.Stamp, bagNS)
	return model.OdomSample{BagNS: bagNS, SourceNS: stamp, Linear: *x, Angular: *z}, err
}

func parseServo(raw []byte, bagNS int64) (model.ServoResult, error) {
	var value struct {
		Header struct {
			Stamp json.Number `json:"stamp"`
		} `json:"header"`
		Status struct {
			GoalID struct {
				ID string `json:"id"`
			} `json:"goal_id"`
			Status uint8  `json:"status"`
			Text   string `json:"text"`
		} `json:"status"`
		Result struct {
			TagID    int32    `json:"tag_id"`
			VSType   int32    `json:"vs_type"`
			Duration *float64 `json:"time_comsume"`
			Pose     struct {
				Position struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
					Z float64 `json:"z"`
				} `json:"position"`
				Orientation struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
					Z float64 `json:"z"`
					W float64 `json:"w"`
				} `json:"orientation"`
			} `json:"baseToTarget"`
		} `json:"result"`
	}
	var result model.ServoResult
	if err := required(raw, "header", "status", "result"); err != nil {
		return result, err
	}
	var object map[string]json.RawMessage
	json.Unmarshal(raw, &object)
	if err := required(object["result"], "tag_id", "vs_type", "time_comsume", "baseToTarget"); err != nil {
		return result, err
	}
	var inner, pose map[string]json.RawMessage
	if err := json.Unmarshal(object["result"], &inner); err != nil {
		return result, err
	}
	if err := required(inner["baseToTarget"], "position", "orientation"); err != nil {
		return result, err
	}
	if err := json.Unmarshal(inner["baseToTarget"], &pose); err != nil {
		return result, err
	}
	if err := required(pose["position"], "x", "y", "z"); err != nil {
		return result, err
	}
	if err := required(pose["orientation"], "x", "y", "z", "w"); err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return result, err
	}
	p, q := value.Result.Pose.Position, value.Result.Pose.Orientation
	if value.Result.Duration == nil || *value.Result.Duration < 0 || !model.Finite(*value.Result.Duration, p.X, p.Y, p.Z, q.X, q.Y, q.Z, q.W) {
		return result, fmt.Errorf("伺服结果的位姿或耗时数据无效")
	}
	stamp, err := stampOrBag(value.Header.Stamp, bagNS)
	if err != nil {
		return result, err
	}
	canonical, err := model.CanonicalJSON(raw)
	if err != nil {
		return result, err
	}
	result = model.ServoResult{Event: model.Event{TimestampNS: stamp, EventKey: model.Hash("servo", value.Status.GoalID.ID, fmt.Sprint(stamp), canonical)},
		GoalID: value.Status.GoalID.ID, Status: value.Status.Status, StatusText: value.Status.Text,
		TagID: value.Result.TagID, VSType: value.Result.VSType, Duration: *value.Result.Duration,
		X: p.X, Y: p.Y, Z: p.Z, QX: q.X, QY: q.Y, QZ: q.Z, QW: q.W, RawData: canonical}
	return result, nil
}
