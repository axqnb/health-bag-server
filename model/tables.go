package model

import "time"

type Task struct {
	ID              uint64    `gorm:"primaryKey" json:"-"`
	TaskKey         string    `gorm:"type:char(64);uniqueIndex;not null;comment:车辆ID、起终点和起止时间生成的任务唯一标识" json:"task_key"`
	DataDigest      string    `gorm:"type:char(64);not null;comment:任务业务结果的SHA-256摘要，用于比对重复导入" json:"-"`
	RobotID         int64     `gorm:"not null;index:idx_health_robot_time,priority:1;comment:接收目录配置对应的车辆ID" json:"robot_id"`
	MapName         *string   `gorm:"size:128;index:idx_health_map_finish,priority:1;comment:任务所在地图名称，NULL表示未知" json:"map_name"`
	IsLoading       *bool     `gorm:"comment:车辆是否带载，NULL表示未知" json:"is_loading"`
	StartID         uint32    `gorm:"type:bigint;not null;comment:任务起点ID" json:"start_id"`
	FinishID        uint32    `gorm:"type:bigint;not null;index:idx_health_map_finish,priority:2;comment:任务终点ID" json:"finish_id"`
	StartTimestamp  string    `gorm:"type:decimal(20,9);not null;index:idx_health_robot_time,priority:2;index:idx_health_map_finish,priority:3;comment:任务开始时间戳，单位秒" json:"start_timestamp"`
	EndTimestamp    string    `gorm:"type:decimal(20,9);not null;comment:任务结束时间戳，单位秒" json:"end_timestamp"`
	AnalysisVersion string    `gorm:"size:32;not null;comment:生成指标时使用的分析版本" json:"analysis_version"`
	Quality         string    `gorm:"type:json;not null;comment:数据质量说明，含话题计数、警告和分析配置" json:"quality"`
	CreatedAt       time.Time `gorm:"comment:记录创建时间" json:"-"`
}

func (Task) TableName() string { return "health_task" }

type Event struct {
	ID          uint64 `gorm:"primaryKey" json:"-"`
	TaskID      uint64 `gorm:"not null;index;comment:所属任务ID，对应health_task.id" json:"-"`
	EventKey    string `gorm:"type:char(64);uniqueIndex;not null;comment:事件唯一标识，用于去重" json:"event_key"`
	TimestampNS int64  `gorm:"not null;comment:事件时间戳，单位纳秒" json:"timestamp_ns"`
}

type PoseError struct {
	X   float64 `gorm:"comment:车端上报的X方向分量原值" json:"x"`
	Y   float64 `gorm:"comment:车端上报的Y方向分量原值" json:"y"`
	Yaw float64 `gorm:"comment:车端上报的偏航角分量原值" json:"yaw"`
}

type ReachPrecision struct {
	Event            `gorm:"embedded"`
	Control          PoseError `gorm:"embedded;embeddedPrefix:control_error_" json:"control_error"`
	ClosedLoop       PoseError `gorm:"embedded;embeddedPrefix:closedloop_error_" json:"closedloop_error"`
	Locate           PoseError `gorm:"embedded;embeddedPrefix:locate_error_" json:"locate_error"`
	CompensationType int       `gorm:"comment:车端上报的到点补偿类型编码" json:"compensation_type"`
	RawData          string    `gorm:"type:json;not null;comment:车端上报的到点事件JSON" json:"raw_data"`
}

func (ReachPrecision) TableName() string { return "health_reach_precision" }

type Stats struct {
	Mean *float64 `gorm:"comment:样本绝对值的均值，NULL表示无有效样本" json:"mean"`
	Max  *float64 `gorm:"comment:样本绝对值的最大值，NULL表示无有效样本" json:"max"`
	P95  *float64 `gorm:"comment:样本绝对值的第95百分位，NULL表示无有效样本" json:"p95"`
}

type ProcessControl struct {
	TaskID             uint64 `gorm:"primaryKey;autoIncrement:false;comment:所属任务ID，对应health_task.id" json:"-"`
	Status             string `gorm:"size:32;not null;comment:过程控制统计状态" json:"status"`
	SampleCount        int64  `gorm:"comment:过程控制样本数" json:"sample_count"`
	LateralExceedCount int64  `gorm:"comment:横向误差绝对值超过阈值的样本数" json:"lateral_exceed_count"`
	HeadingExceedCount int64  `gorm:"comment:航向误差绝对值超过阈值的样本数" json:"heading_exceed_count"`
	LinearError        Stats  `gorm:"embedded;embeddedPrefix:linear_error_" json:"linear_error"`
	AngularError       Stats  `gorm:"embedded;embeddedPrefix:angular_error_" json:"angular_error"`
	JumpCount          *int64 `gorm:"comment:按车端累计计数增量统计的定位跳动次数" json:"jump_count"`
}

func (ProcessControl) TableName() string { return "health_process_control" }

type Twisting struct {
	TaskID                uint64   `gorm:"primaryKey;autoIncrement:false;comment:所属任务ID，对应health_task.id" json:"-"`
	Status                string   `gorm:"size:32;not null;comment:频谱提取及扭动判定的计算状态" json:"status"`
	SampleCount           int64    `gorm:"comment:任务内角速度样本数，去重和重采样前" json:"sample_count"`
	AngularVelocity       Stats    `gorm:"embedded;embeddedPrefix:angular_velocity_" json:"angular_velocity"`
	HasTwisting           *bool    `gorm:"comment:是否检测到扭动事件，NULL表示未判定" json:"has_twisting"`
	EpisodeCount          *int64   `gorm:"comment:满足最短持续时间的扭动事件数" json:"episode_count"`
	DominantFreq          *float64 `gorm:"comment:任务内最大频谱峰值对应的频率，单位Hz" json:"dominant_freq"`
	MainFreqDominant      *float64 `gorm:"comment:任务内最大角速度频谱幅值，单位rad/s" json:"main_freq_dominant"`
	TwistingDurationTotal *float64 `gorm:"comment:所有扭动事件的总时长，单位秒" json:"twisting_duration_total"`
	TwistingDurationMax   *float64 `gorm:"comment:单次扭动事件的最长时长，单位秒" json:"twisting_duration_max"`
}

func (Twisting) TableName() string { return "health_twisting" }

type ActuatorTracking struct {
	ID               uint64   `gorm:"primaryKey" json:"-"`
	TaskID           uint64   `gorm:"not null;uniqueIndex:idx_health_tracking_channel,priority:1;comment:所属任务ID，对应health_task.id" json:"-"`
	Channel          string   `gorm:"size:64;not null;uniqueIndex:idx_health_tracking_channel,priority:2;comment:配置的执行器分析通道名称" json:"channel"`
	Quantity         string   `gorm:"size:16;not null;comment:通道物理量类型，取值为linear、angular或motor" json:"quantity"`
	Status           string   `gorm:"size:32;not null;comment:执行器指令与反馈的配对状态" json:"status"`
	SampleCount      int64    `gorm:"comment:成功匹配指令的反馈样本数" json:"sample_count"`
	UnmatchedCount   int64    `gorm:"comment:未能匹配指令的反馈样本数" json:"unmatched_count"`
	SpeedExceedCount int64    `gorm:"comment:指令与反馈误差绝对值超过阈值的样本数" json:"speed_exceed_count"`
	SpeedCmd         Stats    `gorm:"embedded;embeddedPrefix:speed_cmd_" json:"speed_cmd"`
	SpeedFeedback    Stats    `gorm:"embedded;embeddedPrefix:speed_feedback_" json:"speed_feedback"`
	SpeedError       Stats    `gorm:"embedded;embeddedPrefix:speed_error_" json:"speed_error"`
	ResponseDelay    *float64 `gorm:"comment:反馈达到配置目标比例的平均阶跃响应时间，单位秒" json:"response_delay"`
}

func (ActuatorTracking) TableName() string { return "health_actuator_tracking" }

type ServoResult struct {
	Event      `gorm:"embedded"`
	GoalID     string   `gorm:"size:255;comment:ROS伺服Action目标ID" json:"goal_id"`
	Status     uint8    `gorm:"comment:ROS伺服Action结果状态码" json:"status"`
	StatusText string   `gorm:"type:text;comment:ROS伺服Action结果状态说明" json:"status_text"`
	TagID      int32    `gorm:"comment:伺服目标标签ID" json:"tag_id"`
	VSType     int32    `gorm:"comment:车端上报的伺服类型编码" json:"vs_type"`
	Duration   float64  `gorm:"comment:车端上报的伺服执行耗时原值" json:"servo_duration"`
	X          float64  `gorm:"comment:原始伺服位姿平移X分量" json:"x"`
	Y          float64  `gorm:"comment:原始伺服位姿平移Y分量" json:"y"`
	Z          float64  `gorm:"comment:原始伺服位姿平移Z分量" json:"z"`
	QX         float64  `gorm:"comment:原始伺服位姿四元数X分量" json:"qx"`
	QY         float64  `gorm:"comment:原始伺服位姿四元数Y分量" json:"qy"`
	QZ         float64  `gorm:"comment:原始伺服位姿四元数Z分量" json:"qz"`
	QW         float64  `gorm:"comment:原始伺服位姿四元数W分量" json:"qw"`
	ErrorX     *float64 `gorm:"comment:相对期望伺服位姿的X方向误差，NULL表示未计算" json:"servo_error_x"`
	ErrorY     *float64 `gorm:"comment:相对期望伺服位姿的Y方向误差，NULL表示未计算" json:"servo_error_y"`
	ErrorZ     *float64 `gorm:"comment:相对期望伺服位姿的Z方向误差，NULL表示未计算" json:"servo_error_z"`
	ErrorYaw   *float64 `gorm:"comment:相对期望伺服位姿的偏航角误差，单位rad，NULL表示未计算" json:"servo_error_yaw"`
	RawData    string   `gorm:"type:json;not null;comment:ROS伺服结果消息解码后的JSON" json:"raw_data"`
}

func (ServoResult) TableName() string { return "health_servo_result" }
