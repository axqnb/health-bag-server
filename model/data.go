package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
)

const (
	MissionTopic    = "/nav_manager/mission_info"
	ReachTopic      = "/nav_manager/nav_reach_precision"
	ControlTopic    = "/nav_manager/process_control_error"
	OdomTopic       = "/emma_odom"
	CommandTopic    = "/mux_vel"
	FeedbackTopic   = "/jz_cmd_feedback"
	ServoTopic      = "/ibvs_constrained_action/result"
	AnalysisVersion = "1.0.0"
)

type Mission struct {
	StartID  uint32      `json:"start_id"`
	FinishID uint32      `json:"finish_id"`
	Start    json.Number `json:"start_timestamp"`
	End      json.Number `json:"end_timestamp"`
	StartNS  int64       `json:"-"`
	EndNS    int64       `json:"-"`
}

func SecondsToNS(value json.Number) (int64, error) {
	text := string(value)
	if len(text) == 0 || len(text) > 64 {
		return 0, fmt.Errorf("时间戳格式无效：%q", text)
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || r.Sign() < 0 {
		return 0, fmt.Errorf("时间戳格式无效：%q", text)
	}
	r.Mul(r, big.NewRat(1000000000, 1))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if !n.IsInt64() {
		return 0, fmt.Errorf("时间戳超出支持范围")
	}
	return n.Int64(), nil
}

func FormatSeconds(ns int64) string { return fmt.Sprintf("%d.%09d", ns/1000000000, ns%1000000000) }

func Hash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%d:", len(part))
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m Mission) Key(robotID int64) string {
	return Hash(fmt.Sprint(robotID), fmt.Sprint(m.StartID), fmt.Sprint(m.FinishID), FormatSeconds(m.StartNS), FormatSeconds(m.EndNS))
}

func CanonicalJSON(raw []byte) (string, error) {
	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func Finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func Float(value float64) *float64         { return &value }
func Bool(value bool) *bool                { return &value }
func NormalizeAngle(value float64) float64 { return math.Atan2(math.Sin(value), math.Cos(value)) }

type ControlSample struct {
	BagNS     int64
	SourceNS  int64
	Y         float64
	Yaw       float64
	JumpCount uint32
}
type OdomSample struct {
	BagNS    int64
	SourceNS int64
	Linear   float64
	Angular  float64
}
type Motor struct {
	Type uint8     `json:"type"`
	Data []float64 `json:"data"`
}
type Actuator struct {
	Type   uint8   `json:"type"`
	Motors []Motor `json:"motors"`
}
type ActuatorFrame struct {
	BagNS     int64
	Stamp     json.Number `json:"stamp"`
	Actuators []Actuator  `json:"actuators"`
}

type Data struct {
	Mission     Mission
	Reach       []ReachPrecision
	Control     []ControlSample
	Odom        []OdomSample
	Commands    []ActuatorFrame
	Feedback    []ActuatorFrame
	Servo       []ServoResult
	TopicCounts map[string]uint64
	Warnings    []string
}

type Bundle struct {
	Task     Task               `json:"task"`
	Reach    []ReachPrecision   `json:"reach"`
	Process  ProcessControl     `json:"process"`
	Twisting Twisting           `json:"twisting"`
	Tracking []ActuatorTracking `json:"tracking"`
	Servo    []ServoResult      `json:"servo"`
}

func (b *Bundle) Digest() (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return Hash(string(data)), nil
}
