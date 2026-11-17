package model

import "time"

type Task struct {
	ID              uint64    `gorm:"primaryKey" json:"-"`
	TaskKey         string    `gorm:"type:char(64);uniqueIndex;not null" json:"task_key"`
	DataDigest      string    `gorm:"type:char(64);not null" json:"-"`
	RobotID         int64     `gorm:"not null;index:idx_health_robot_time,priority:1" json:"robot_id"`
	MapName         *string   `gorm:"size:128;index:idx_health_map_finish,priority:1" json:"map_name"`
	IsLoading       *bool     `json:"is_loading"`
	StartID         uint32    `gorm:"type:bigint;not null" json:"start_id"`
	FinishID        uint32    `gorm:"type:bigint;not null;index:idx_health_map_finish,priority:2" json:"finish_id"`
	StartTimestamp  string    `gorm:"type:decimal(20,9);not null;index:idx_health_robot_time,priority:2;index:idx_health_map_finish,priority:3" json:"start_timestamp"`
	EndTimestamp    string    `gorm:"type:decimal(20,9);not null" json:"end_timestamp"`
	AnalysisVersion string    `gorm:"size:32;not null" json:"analysis_version"`
	Quality         string    `gorm:"type:json;not null" json:"quality"`
	CreatedAt       time.Time `json:"-"`
}

func (Task) TableName() string { return "health_task" }

type Event struct {
	ID          uint64 `gorm:"primaryKey" json:"-"`
	TaskID      uint64 `gorm:"not null;index" json:"-"`
	EventKey    string `gorm:"type:char(64);uniqueIndex;not null" json:"event_key"`
	TimestampNS int64  `gorm:"not null" json:"timestamp_ns"`
}

type PoseError struct {
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	Yaw float64 `json:"yaw"`
}

type ReachPrecision struct {
	Event            `gorm:"embedded"`
	Control          PoseError `gorm:"embedded;embeddedPrefix:control_error_" json:"control_error"`
	ClosedLoop       PoseError `gorm:"embedded;embeddedPrefix:closedloop_error_" json:"closedloop_error"`
	Locate           PoseError `gorm:"embedded;embeddedPrefix:locate_error_" json:"locate_error"`
	CompensationType int       `json:"compensation_type"`
	RawData          string    `gorm:"type:json;not null" json:"raw_data"`
}

func (ReachPrecision) TableName() string { return "health_reach_precision" }

type Stats struct {
	Mean *float64 `json:"mean"`
	Max  *float64 `json:"max"`
	P95  *float64 `json:"p95"`
}

type ProcessControl struct {
	TaskID             uint64 `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Status             string `gorm:"size:32;not null" json:"status"`
	SampleCount        int64  `json:"sample_count"`
	LateralExceedCount int64  `json:"lateral_exceed_count"`
	HeadingExceedCount int64  `json:"heading_exceed_count"`
	LinearError        Stats  `gorm:"embedded;embeddedPrefix:linear_error_" json:"linear_error"`
	AngularError       Stats  `gorm:"embedded;embeddedPrefix:angular_error_" json:"angular_error"`
	JumpCount          *int64 `json:"jump_count"`
}

func (ProcessControl) TableName() string { return "health_process_control" }

type Twisting struct {
	TaskID                uint64   `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Status                string   `gorm:"size:32;not null" json:"status"`
	SampleCount           int64    `json:"sample_count"`
	AngularVelocity       Stats    `gorm:"embedded;embeddedPrefix:angular_velocity_" json:"angular_velocity"`
	HasTwisting           *bool    `json:"has_twisting"`
	EpisodeCount          *int64   `json:"episode_count"`
	DominantFreq          *float64 `json:"dominant_freq"`
	MainFreqDominant      *float64 `json:"main_freq_dominant"`
	TwistingDurationTotal *float64 `json:"twisting_duration_total"`
	TwistingDurationMax   *float64 `json:"twisting_duration_max"`
}

func (Twisting) TableName() string { return "health_twisting" }

type ActuatorTracking struct {
	ID               uint64   `gorm:"primaryKey" json:"-"`
	TaskID           uint64   `gorm:"not null;uniqueIndex:idx_health_tracking_channel,priority:1" json:"-"`
	Channel          string   `gorm:"size:64;not null;uniqueIndex:idx_health_tracking_channel,priority:2" json:"channel"`
	Quantity         string   `gorm:"size:16;not null" json:"quantity"`
	Status           string   `gorm:"size:32;not null" json:"status"`
	SampleCount      int64    `json:"sample_count"`
	UnmatchedCount   int64    `json:"unmatched_count"`
	SpeedExceedCount int64    `json:"speed_exceed_count"`
	SpeedCmd         Stats    `gorm:"embedded;embeddedPrefix:speed_cmd_" json:"speed_cmd"`
	SpeedFeedback    Stats    `gorm:"embedded;embeddedPrefix:speed_feedback_" json:"speed_feedback"`
	SpeedError       Stats    `gorm:"embedded;embeddedPrefix:speed_error_" json:"speed_error"`
	ResponseDelay    *float64 `json:"response_delay"`
}

func (ActuatorTracking) TableName() string { return "health_actuator_tracking" }

type ServoResult struct {
	Event      `gorm:"embedded"`
	GoalID     string   `gorm:"size:255" json:"goal_id"`
	Status     uint8    `json:"status"`
	StatusText string   `gorm:"type:text" json:"status_text"`
	TagID      int32    `json:"tag_id"`
	VSType     int32    `json:"vs_type"`
	Duration   float64  `json:"servo_duration"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Z          float64  `json:"z"`
	QX         float64  `json:"qx"`
	QY         float64  `json:"qy"`
	QZ         float64  `json:"qz"`
	QW         float64  `json:"qw"`
	ErrorX     *float64 `json:"servo_error_x"`
	ErrorY     *float64 `json:"servo_error_y"`
	ErrorZ     *float64 `json:"servo_error_z"`
	ErrorYaw   *float64 `json:"servo_error_yaw"`
	RawData    string   `gorm:"type:json;not null" json:"raw_data"`
}

func (ServoResult) TableName() string { return "health_servo_result" }
