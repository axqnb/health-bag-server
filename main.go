package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"health-bag-server/command"
	"health-bag-server/config"
	"health-bag-server/server/database"
	"health-bag-server/service/ingest"
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

	// 加载配置；检查命令独立执行，不初始化数据库。
	opts, err := command.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}
	if opts.CheckConfig {
		return command.CheckConfig(cfg, os.Stdout)
	}
	if opts.CheckBag != "" {
		return command.CheckBag(cfg, opts, os.Stdout)
	}
	if err := cfg.ValidateMysql(); err != nil {
		return err
	}

	// 初始化日志和数据库。
	mcs.InitLog("health-bag-server")
	mcs.InitMysql(cfg.Mysql, false)
	defer database.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 初始化业务表。
	repository := &database.Repository{DB: mcs.DB}
	if cfg.AutoMigrate || opts.MigrateOnly {
		if err := repository.Migrate(ctx); err != nil {
			return fmt.Errorf("初始化业务数据表失败：%w", err)
		}
	}
	if opts.MigrateOnly {
		mcs.Log.Infof("数据库业务表初始化完成")
		return nil
	}

	// 启动导入服务，收到退出信号后等待 worker 退出。
	return ingest.Start(ctx, cfg, repository)
}
