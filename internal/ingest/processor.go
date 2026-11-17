package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"health-bag-server/internal/analysis"
	"health-bag-server/internal/config"
	"health-bag-server/internal/model"
	"server01.jz/cloudia-module/mcs"
)

type BagReader interface {
	Read(context.Context, string) (*model.Data, error)
}
type Saver interface {
	Save(context.Context, *model.Bundle) (bool, error)
}

type Processor struct {
	Reader BagReader
	Store  Saver
	Config config.Config
}

type snapshot struct {
	path, original, digest string
	signature              os.FileInfo
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func info(format string, args ...interface{}) {
	if mcs.Log != nil {
		mcs.Log.Infof(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
func warn(format string, args ...interface{}) {
	if mcs.Log != nil {
		mcs.Log.Warnf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (p *Processor) copy(ctx context.Context, path string, input config.Input) (*snapshot, error) {
	if strings.ToLower(filepath.Ext(path)) != ".bag" {
		return nil, fmt.Errorf("只能处理已完成录制的 .bag 文件")
	}
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !lstat.Mode().IsRegular() {
		return nil, fmt.Errorf("原始bag必须是普通文件，不能使用符号链接")
	}
	root, err := filepath.EvalSymlinks(input.Directory)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	if !contained(root, real) {
		return nil, fmt.Errorf("bag文件不在配置的接收目录内")
	}
	source, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if before.Size() > p.Config.MaxBagBytes {
		return nil, fmt.Errorf("原始bag超过程序支持的文件大小上限")
	}
	if err := os.MkdirAll(p.Config.WorkDir, 0750); err != nil {
		return nil, err
	}
	work, err := filepath.EvalSymlinks(p.Config.WorkDir)
	if err != nil {
		return nil, err
	}
	work, err = filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	if work == root || contained(root, work) {
		return nil, fmt.Errorf("临时副本目录不能位于接收目录内")
	}
	temp, err := os.CreateTemp(p.Config.WorkDir, "processing-*.bag")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		temp.Close()
		if !ok {
			os.Remove(temp.Name())
		}
	}()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(contextReader{ctx, source}, p.Config.MaxBagBytes+1))
	if err != nil {
		return nil, err
	}
	if n > p.Config.MaxBagBytes {
		return nil, fmt.Errorf("上传中的bag已超过程序支持的文件大小上限")
	}
	after, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !sameFile(before, after) || n != before.Size() {
		return nil, fmt.Errorf("复制期间源文件发生变化，等待FTP上传完成后重试")
	}
	if err := temp.Sync(); err != nil {
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	ok = true
	return &snapshot{temp.Name(), real, hex.EncodeToString(hasher.Sum(nil)), before}, nil
}

func (p *Processor) Process(ctx context.Context, path string, input config.Input) (err error) {
	started := time.Now()
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("处理bag时发生异常：%v", value)
		}
		if err != nil {
			warn("bag处理未完成，保留原文件等待重试：文件=%q，车辆ID=%d，耗时=%s，原因=%v", path, input.RobotID, time.Since(started), err)
		}
	}()
	snap, err := p.copy(ctx, path, input)
	if err != nil {
		return err
	}
	defer os.Remove(snap.path)
	data, err := p.Reader.Read(ctx, snap.path)
	if err != nil {
		return fmt.Errorf("读取或解析bag失败：%w", err)
	}
	bundle, err := analysis.Build(ctx, data, input, p.Config.Analysis)
	if err != nil {
		return fmt.Errorf("计算任务指标失败：%w", err)
	}
	inserted, err := p.Store.Save(ctx, bundle)
	if err != nil {
		return fmt.Errorf("数据库事务提交失败：%w", err)
	}
	result := "重复任务，业务数据一致，无需重复写入"
	if inserted {
		result = "新任务，业务数据已写入"
	}
	info("任务数据入库成功：文件=%q，车辆ID=%d，任务标识=%s，处理结果=%s，文件SHA256=%s，耗时=%s", path, input.RobotID, bundle.Task.TaskKey, result, snap.digest, time.Since(started))
	if notes := qualityNotes(bundle.Task.Quality); notes != "" {
		info("任务数据说明：车辆ID=%d，任务标识=%s，%s", input.RobotID, bundle.Task.TaskKey, notes)
	}
	if err := removeCommitted(ctx, snap); err != nil {
		return fmt.Errorf("任务数据已提交，但原始bag尚未删除：%w", err)
	}
	info("入库后删除bag成功：文件=%q，车辆ID=%d，任务标识=%s，耗时=%s", path, input.RobotID, bundle.Task.TaskKey, time.Since(started))
	return nil
}

func qualityNotes(raw string) string {
	var quality struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(raw), &quality); err != nil {
		return "详细数据说明无法解析"
	}
	labels := map[string]string{
		"map_name_unknown":                       "未提供任务期间的地图名称，按未知保存",
		"is_loading_unknown":                     "未提供任务期间的载荷状态，按未知保存",
		"localization_counter_reset_within_task": "定位跳动计数在任务期间发生复位，已分段统计",
		"reach_values_follow_vehicle_learning_semantics_not_verified_residuals": "到点数据按车端自学习原值保存，尚未确认为本次到点实测残差",
		"servo_event_outside_navigation_window":                                 "伺服结果时间超出导航任务时间范围",
		"servo_error_requires_expected_pose":                                    "未提供期望对接位姿，仅保存伺服原始位姿和耗时",
		"servo_orientation_invalid":                                             "伺服方向四元数无效，未计算对接误差",
		"non_finite_ros_fields":                                                 "部分ROS字段包含非有限数值，已按未知处理",
	}
	var notes []string
	for _, code := range quality.Warnings {
		if text, ok := labels[code]; ok {
			notes = append(notes, text)
		} else {
			notes = append(notes, "数据说明代码："+code)
		}
	}
	return strings.Join(notes, "；")
}

func removeCommitted(ctx context.Context, snap *snapshot) error {
	f, err := os.Open(snap.original)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !sameFile(before, snap.signature) {
		return fmt.Errorf("生成临时副本后源文件被替换或修改，本次不删除")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, f}); err != nil {
		return err
	}
	after, err := os.Stat(snap.original)
	if err != nil {
		return err
	}
	if !sameFile(before, after) || hex.EncodeToString(hash.Sum(nil)) != snap.digest {
		return fmt.Errorf("源文件内容与解析副本不一致，本次不删除")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(snap.original)
}
