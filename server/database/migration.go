package database

import (
	"context"
	"fmt"

	"health-bag-server/model"
)

func (r *Repository) Migrate(ctx context.Context) error {
	tables := []struct {
		model   interface{ TableName() string }
		comment string
	}{
		{&model.Task{}, "AGV健康分析任务主表，记录任务身份、起止信息和数据质量"},
		{&model.ReachPrecision{}, "车辆到点事件，记录车端上报的三组到点偏差和补偿类型"},
		{&model.ProcessControl{}, "任务过程控制汇总，记录横向及航向误差和定位跳动"},
		{&model.Twisting{}, "任务角速度频谱及扭动事件统计"},
		{&model.ActuatorTracking{}, "任务内各执行器通道的指令反馈、跟踪误差和响应统计"},
		{&model.ServoResult{}, "伺服执行结果事件，记录状态、原始位姿和相对期望位姿的误差"},
	}

	db := r.DB.WithContext(ctx)
	for _, table := range tables {
		// 表注释来自上面的固定文案，与 MySQL 驱动生成字段 COMMENT 使用相同的引号规则。
		commentSQL := db.Dialector.Explain("COMMENT = ?", table.comment)
		options := "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 " + commentSQL
		if err := db.Set("gorm:table_options", options).AutoMigrate(table.model); err != nil {
			return fmt.Errorf("迁移表 %s：%w", table.model.TableName(), err)
		}
	}
	return nil
}
