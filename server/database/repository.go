package database

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"health-bag-server/model"
)

var ErrConflict = errors.New("同一任务已存在但业务数据不一致，保留原始bag等待核对")

type Repository struct{ DB *gorm.DB }

func (r *Repository) Migrate(ctx context.Context) error {
	return r.DB.WithContext(ctx).Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").AutoMigrate(
		&model.Task{}, &model.ReachPrecision{}, &model.ProcessControl{}, &model.Twisting{}, &model.ActuatorTracking{}, &model.ServoResult{})
}

// Save commits the whole task, including all metrics, or nothing. The task's
// digest describes parsed business data, not a bag/file import history record.
func (r *Repository) Save(ctx context.Context, bundle *model.Bundle) (inserted bool, err error) {
	if r.DB == nil {
		return false, fmt.Errorf("数据库尚未初始化")
	}
	digest, err := bundle.Digest()
	if err != nil {
		return false, err
	}
	if bundle.Task.TaskKey == "" || digest != bundle.Task.DataDigest {
		return false, fmt.Errorf("任务标识或业务数据校验摘要无效")
	}
	err = r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task := bundle.Task
		task.ID = 0
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&task)
		if result.Error != nil {
			return result.Error
		}
		var stored model.Task
		if err := tx.Where("task_key = ?", task.TaskKey).Take(&stored).Error; err != nil {
			return err
		}
		if stored.DataDigest != task.DataDigest {
			return ErrConflict
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("任务写入返回的影响行数异常：%d", result.RowsAffected)
		}
		process, twisting := bundle.Process, bundle.Twisting
		process.TaskID = stored.ID
		twisting.TaskID = stored.ID
		if err := tx.Create(&process).Error; err != nil {
			return err
		}
		if err := tx.Create(&twisting).Error; err != nil {
			return err
		}
		reach := append([]model.ReachPrecision{}, bundle.Reach...)
		for i := range reach {
			reach[i].ID = 0
			reach[i].TaskID = stored.ID
		}
		if len(reach) > 0 {
			if err := tx.CreateInBatches(&reach, 200).Error; err != nil {
				return err
			}
		}
		tracking := append([]model.ActuatorTracking{}, bundle.Tracking...)
		for i := range tracking {
			tracking[i].ID = 0
			tracking[i].TaskID = stored.ID
		}
		if len(tracking) > 0 {
			if err := tx.CreateInBatches(&tracking, 200).Error; err != nil {
				return err
			}
		}
		servo := append([]model.ServoResult{}, bundle.Servo...)
		for i := range servo {
			servo[i].ID = 0
			servo[i].TaskID = stored.ID
		}
		if len(servo) > 0 {
			if err := tx.CreateInBatches(&servo, 200).Error; err != nil {
				return err
			}
		}
		inserted = true
		return nil
	})
	if err != nil {
		inserted = false
	}
	return inserted, err
}
