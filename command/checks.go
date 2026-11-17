package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"health-bag-server/config"
	"health-bag-server/service/analysis"
	"health-bag-server/service/bag"
)

// CheckConfig 检查数据库配置格式，不建立数据库连接。
func CheckConfig(cfg config.Config, output io.Writer) error {
	if err := cfg.ValidateMysql(); err != nil {
		return err
	}
	fmt.Fprintln(output, "配置格式检查通过，未连接数据库")
	return nil
}

// CheckBag 解析并分析单个 bag，输出 JSON，不入库也不删除文件。
func CheckBag(cfg config.Config, opts Options, output io.Writer) error {
	reader := bag.Reader{MaxBytes: cfg.MaxBagBytes, MaxMessages: cfg.MaxMessages}
	var input *config.Input
	for i := range cfg.Inputs {
		if opts.RobotID == 0 && len(cfg.Inputs) == 1 || cfg.Inputs[i].RobotID == opts.RobotID {
			input = &cfg.Inputs[i]
			break
		}
	}
	if input == nil {
		return fmt.Errorf("请通过 -robot-id 指定与接收目录配置一致的车辆ID")
	}
	data, err := reader.Read(context.Background(), opts.CheckBag)
	if err != nil {
		return err
	}
	bundle, err := analysis.Build(context.Background(), data, *input, cfg.Analysis)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(bundle)
}
