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

	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	configPath := flags.String("config", "health_server.yml", "配置文件路径")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "用法：%s [-config 配置文件路径]\n", flags.Name())
		flags.PrintDefaults()
	}
	if err := flags.Parse(os.Args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}

	// 加载并校验配置。
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
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
	if cfg.AutoMigrate {
		if err := repository.Migrate(ctx); err != nil {
			return fmt.Errorf("初始化业务数据表失败：%w", err)
		}
	}

	// 启动导入服务，收到退出信号后等待 worker 退出。
	return ingest.Start(ctx, cfg, repository)
}
