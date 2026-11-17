package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"health-bag-server/internal/analysis"
	"health-bag-server/internal/bag"
	"health-bag-server/internal/config"
	"health-bag-server/internal/database"
	"health-bag-server/internal/ingest"
	"server01.jz/cloudia-module/mcs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "健康数据服务启动或运行失败：", err)
		os.Exit(1)
	}
}

func run() (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("服务启动或运行异常：%v", value)
		}
	}()
	time.Local = time.UTC
	configPath := flag.String("config", "health_server.yml", "配置文件路径")
	checkConfig := flag.Bool("check-config", false, "检查配置，不连接数据库")
	checkBag := flag.String("check-bag", "", "检查一个bag并打印解析结果，不连接数据库、不删除文件")
	robotID := flag.Int64("robot-id", 0, "检查bag时使用的车辆ID，需要与接收目录配置一致")
	migrateOnly := flag.Bool("migrate-only", false, "初始化业务数据表后退出")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：%s [参数]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *checkConfig {
		if err := cfg.ValidateMysql(); err != nil {
			return err
		}
		fmt.Println("配置格式检查通过，未连接数据库")
		return nil
	}
	reader := bag.Reader{MaxBytes: cfg.MaxBagBytes, MaxMessages: cfg.MaxMessages}
	if *checkBag != "" {
		var input *config.Input
		for i := range cfg.Inputs {
			if *robotID == 0 && len(cfg.Inputs) == 1 || cfg.Inputs[i].RobotID == *robotID {
				input = &cfg.Inputs[i]
				break
			}
		}
		if input == nil {
			return fmt.Errorf("请通过 -robot-id 指定与接收目录配置一致的车辆ID")
		}
		data, err := reader.Read(context.Background(), *checkBag)
		if err != nil {
			return err
		}
		bundle, err := analysis.Build(context.Background(), data, *input, cfg.Analysis)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(bundle)
	}
	if err := cfg.ValidateMysql(); err != nil {
		return err
	}
	mcs.InitLog("health-bag-server")
	mcs.InitMysql(cfg.Mysql, false)
	defer func() {
		if db, err := mcs.DB.DB(); err == nil {
			db.Close()
		}
		if mcs.SearchDB != nil {
			if db, err := mcs.SearchDB.DB(); err == nil {
				db.Close()
			}
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	repository := &database.Repository{DB: mcs.DB}
	if cfg.AutoMigrate || *migrateOnly {
		if err := repository.Migrate(ctx); err != nil {
			return fmt.Errorf("初始化业务数据表失败：%w", err)
		}
	}
	if *migrateOnly {
		mcs.Log.Infof("数据库业务表初始化完成")
		return nil
	}
	processor := &ingest.Processor{Reader: reader, Store: repository, Config: cfg}
	runner := ingest.Runner{Config: cfg, Handler: processor}
	mcs.Log.Infof("健康数据服务已启动：接收目录数=%d，并发任务数=%d，扫描间隔=%g秒，文件稳定等待=%g秒，失败重试间隔=%g秒；入库成功后删除原始bag", len(cfg.Inputs), cfg.Workers, cfg.ScanSeconds, cfg.StableSeconds, cfg.RetrySeconds)
	err = runner.Run(ctx)
	mcs.Log.Infof("健康数据服务已停止")
	return err
}
