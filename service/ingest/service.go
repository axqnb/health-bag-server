package ingest

import (
	"context"

	"health-bag-server/config"
	"health-bag-server/service/bag"
)

// Start 组装并运行文件导入服务，直到收到取消信号。
func Start(ctx context.Context, cfg config.Config, store Saver) error {
	reader := bag.Reader{MaxBytes: cfg.MaxBagBytes, MaxMessages: cfg.MaxMessages}
	processor := &Processor{Reader: reader, Store: store, Config: cfg}
	runner := Runner{Config: cfg, Handler: processor}
	info("健康数据服务已启动：接收目录数=%d，并发任务数=%d，扫描间隔=%g秒，文件稳定等待=%g秒，失败重试间隔=%g秒；入库成功后删除原始bag", len(cfg.Inputs), cfg.Workers, cfg.ScanSeconds, cfg.StableSeconds, cfg.RetrySeconds)
	err := runner.Run(ctx)
	info("健康数据服务已停止")
	return err
}
