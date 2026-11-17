package command

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Options 保存启动模式与检查命令参数。
type Options struct {
	ConfigPath  string
	CheckConfig bool
	CheckBag    string
	RobotID     int64
	MigrateOnly bool
}

// Parse 解析命令行，将帮助和参数错误写入 output。
func Parse(args []string, output io.Writer) (Options, error) {
	var opts Options
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.ConfigPath, "config", "health_server.yml", "配置文件路径")
	flags.BoolVar(&opts.CheckConfig, "check-config", false, "检查配置，不连接数据库")
	flags.StringVar(&opts.CheckBag, "check-bag", "", "检查一个bag并打印解析结果，不连接数据库、不删除文件")
	flags.Int64Var(&opts.RobotID, "robot-id", 0, "检查bag时使用的车辆ID，需要与接收目录配置一致")
	flags.BoolVar(&opts.MigrateOnly, "migrate-only", false, "初始化业务数据表后退出")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "用法：%s [参数]\n", os.Args[0])
		flags.PrintDefaults()
	}
	err := flags.Parse(args)
	return opts, err
}
