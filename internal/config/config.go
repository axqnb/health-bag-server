package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"server01.jz/cloudia-module/mcs"
)

type Config struct {
	Mysql         *mcs.Sql `yaml:"mysql"`
	AutoMigrate   bool     `yaml:"auto_migrate"`
	WorkDir       string   `yaml:"work_dir"`
	ScanSeconds   float64  `yaml:"scan_seconds"`
	StableSeconds float64  `yaml:"stable_seconds"`
	RetrySeconds  float64  `yaml:"retry_seconds"`
	Workers       int      `yaml:"workers"`
	MaxBagBytes   int64    `yaml:"max_bag_bytes"`
	MaxMessages   uint64   `yaml:"max_messages"`
	Inputs        []Input  `yaml:"inputs"`
	Analysis      Analysis `yaml:"analysis"`
}

type Input struct {
	Directory string  `yaml:"directory"`
	RobotID   int64   `yaml:"robot_id"`
	MapName   *string `yaml:"map_name"`
	IsLoading *bool   `yaml:"is_loading"`
}

type Analysis struct {
	LateralThreshold float64       `yaml:"lateral_threshold_m"`
	HeadingThreshold float64       `yaml:"heading_threshold_deg"`
	Twisting         Twisting      `yaml:"twisting"`
	Tracking         Tracking      `yaml:"tracking"`
	ServoExpected    *ExpectedPose `yaml:"servo_expected_pose"`
}

type Twisting struct {
	MinFrequency       float64  `yaml:"min_frequency_hz"`
	MaxFrequency       float64  `yaml:"max_frequency_hz"`
	AmplitudeThreshold *float64 `yaml:"amplitude_threshold_rad_s"`
	MinDuration        float64  `yaml:"min_duration_seconds"`
	WindowSeconds      float64  `yaml:"window_seconds"`
	MaxGapSeconds      float64  `yaml:"max_gap_seconds"`
}

type ExpectedPose struct {
	X   float64 `yaml:"x"`
	Y   float64 `yaml:"y"`
	Z   float64 `yaml:"z"`
	Yaw float64 `yaml:"yaw_rad"`
}

type Tracking struct {
	UseMessageStamp  bool      `yaml:"use_message_stamp"`
	MaxPairGap       float64   `yaml:"max_pair_gap_seconds"`
	MaxResponseDelay float64   `yaml:"max_response_delay_seconds"`
	Channels         []Channel `yaml:"channels"`
}

type Channel struct {
	Name           string  `yaml:"name"`
	Quantity       string  `yaml:"quantity"`
	Command        []Field `yaml:"command"`
	Feedback       []Field `yaml:"feedback"`
	ErrorThreshold float64 `yaml:"error_threshold"`
	StepThreshold  float64 `yaml:"step_threshold"`
	ReachFraction  float64 `yaml:"reach_fraction"`
}

// A channel is a weighted sum of source fields. This supports direct speed
// fields and configured wheel-to-body kinematics without assuming motor units.
type Field struct {
	ActuatorIndex int     `yaml:"actuator_index"`
	MotorIndex    int     `yaml:"motor_index"`
	DataIndex     int     `yaml:"data_index"`
	ActuatorType  *uint8  `yaml:"actuator_type"`
	MotorType     *uint8  `yaml:"motor_type"`
	Scale         float64 `yaml:"scale"`
}

func Defaults() Config {
	return Config{
		Mysql:       &mcs.Sql{MaxLifeTime: 300, MaxOpenConn: 10, MaxIdleConn: 5},
		AutoMigrate: true, WorkDir: "data/work",
		ScanSeconds: 5, StableSeconds: 60, RetrySeconds: 60, Workers: 1,
		MaxBagBytes: 1024 * 1024 * 1024, MaxMessages: 5000000,
		Analysis: Analysis{
			LateralThreshold: 0.02, HeadingThreshold: 2,
			Twisting: Twisting{MinFrequency: 0.5, MaxFrequency: 10, MinDuration: 1,
				WindowSeconds: 4, MaxGapSeconds: 0.2},
			Tracking: Tracking{MaxPairGap: 0.1, MaxResponseDelay: 2, Channels: []Channel{}},
		},
	}
}

func Load(path string) (cfg Config, err error) {
	cfg = Defaults()
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("读取配置失败：%v", value)
		}
	}()
	if _, err = os.Stat(path); err != nil {
		return cfg, err
	}
	if err = mcs.LoadConfig(path, &cfg); err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		return cfg, fmt.Errorf("临时副本目录不能为空")
	}
	for _, input := range cfg.Inputs {
		if strings.TrimSpace(input.Directory) == "" {
			return cfg, fmt.Errorf("接收目录不能为空")
		}
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, err
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(base, path)
	}
	cfg.WorkDir = resolve(cfg.WorkDir)
	for i := range cfg.Inputs {
		cfg.Inputs[i].Directory = resolve(cfg.Inputs[i].Directory)
	}
	err = cfg.Validate()
	return cfg, err
}

func finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	if !finite(c.ScanSeconds, c.StableSeconds, c.RetrySeconds) || c.ScanSeconds <= 0 || c.StableSeconds < 1 || c.RetrySeconds <= 0 {
		return fmt.Errorf("扫描和重试间隔必须大于零，文件稳定等待时间不能小于1秒")
	}
	if c.Workers < 1 || c.Workers > 32 || c.MaxBagBytes < 4096 || c.MaxMessages < 1 {
		return fmt.Errorf("处理线程数、bag大小上限或消息数量上限无效")
	}
	if c.WorkDir == "" || len(c.Inputs) == 0 {
		return fmt.Errorf("必须设置临时副本目录和至少一个车辆接收目录")
	}
	seen := map[string]bool{}
	for _, in := range c.Inputs {
		if in.RobotID <= 0 || strings.TrimSpace(in.Directory) == "" {
			return fmt.Errorf("每个接收项都需要填写目录，车辆ID必须大于零")
		}
		key := strings.ToLower(filepath.Clean(in.Directory))
		if seen[key] {
			return fmt.Errorf("接收目录重复：%s", in.Directory)
		}
		seen[key] = true
		for _, other := range c.Inputs {
			if other.Directory == in.Directory {
				continue
			}
			rel, err := filepath.Rel(in.Directory, other.Directory)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return fmt.Errorf("不同车辆的接收目录不能重叠或相互包含")
			}
		}
		rel, err := filepath.Rel(in.Directory, c.WorkDir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return fmt.Errorf("临时副本目录必须位于车辆接收目录之外")
		}
	}
	a := c.Analysis
	if !finite(a.LateralThreshold, a.HeadingThreshold) || a.LateralThreshold <= 0 || a.HeadingThreshold <= 0 || a.HeadingThreshold > 180 {
		return fmt.Errorf("过程控制误差阈值无效")
	}
	t := a.Twisting
	if !finite(t.MinFrequency, t.MaxFrequency, t.WindowSeconds, t.MinDuration, t.MaxGapSeconds) || t.MinFrequency <= 0 || t.MaxFrequency <= t.MinFrequency || t.WindowSeconds <= 0 || t.MinDuration <= 0 || t.MaxGapSeconds <= 0 {
		return fmt.Errorf("扭动分析参数无效")
	}
	if t.AmplitudeThreshold != nil && (!finite(*t.AmplitudeThreshold) || *t.AmplitudeThreshold <= 0) {
		return fmt.Errorf("扭动幅值阈值必须大于零，或留空表示暂不判定")
	}
	if a.ServoExpected != nil && !finite(a.ServoExpected.X, a.ServoExpected.Y, a.ServoExpected.Z, a.ServoExpected.Yaw) {
		return fmt.Errorf("期望伺服对接位姿无效")
	}
	if !finite(a.Tracking.MaxPairGap, a.Tracking.MaxResponseDelay) || a.Tracking.MaxPairGap <= 0 || a.Tracking.MaxResponseDelay <= 0 {
		return fmt.Errorf("执行器跟踪时间参数无效")
	}
	names := map[string]bool{}
	for _, ch := range a.Tracking.Channels {
		if ch.Name == "" || len(ch.Name) > 64 || names[ch.Name] {
			return fmt.Errorf("执行器通道名称不能重复，且长度不能超过64字节")
		}
		names[ch.Name] = true
		if ch.Quantity != "linear" && ch.Quantity != "angular" && ch.Quantity != "motor" {
			return fmt.Errorf("通道 %s 的物理量类型无效", ch.Name)
		}
		if !finite(ch.ErrorThreshold, ch.StepThreshold, ch.ReachFraction) || ch.ErrorThreshold <= 0 || ch.StepThreshold <= 0 || ch.ReachFraction <= 0 || ch.ReachFraction > 1 {
			return fmt.Errorf("通道 %s 的误差或响应阈值无效", ch.Name)
		}
		for _, fields := range [][]Field{ch.Command, ch.Feedback} {
			if len(fields) == 0 {
				return fmt.Errorf("通道 %s 必须同时配置指令和反馈的字段映射", ch.Name)
			}
			for _, field := range fields {
				if field.ActuatorIndex < 0 || field.MotorIndex < 0 || field.DataIndex < 0 || !finite(field.Scale) || field.Scale == 0 {
					return fmt.Errorf("通道 %s 的字段索引或单位换算系数无效", ch.Name)
				}
			}
		}
	}
	return nil
}

func (c Config) ValidateMysql() error {
	if c.Mysql == nil || c.Mysql.Address == "" || c.Mysql.User == "" || c.Mysql.DBName == "" {
		return fmt.Errorf("请填写MySQL地址、用户名和数据库名")
	}
	for _, ch := range c.Mysql.DBName {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return fmt.Errorf("MySQL数据库名只能包含字母、数字和下划线")
		}
	}
	return nil
}
