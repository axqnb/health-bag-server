package ingest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"health-bag-server/config"
)

type Handler interface {
	Process(context.Context, string, config.Input) error
}
type job struct {
	path  string
	input config.Input
}
type completed struct {
	path string
	err  error
}
type observed struct {
	size        int64
	modified    time.Time
	stableSince time.Time
	nextTry     time.Time
	inFlight    bool
}

type Runner struct {
	Config  config.Config
	Handler Handler
	files   map[string]*observed
}

func (r *Runner) Run(ctx context.Context) error {
	for _, input := range r.Config.Inputs {
		stat, err := os.Stat(input.Directory)
		if err != nil {
			return fmt.Errorf("无法访问接收目录 %s：%w", input.Directory, err)
		}
		if !stat.IsDir() {
			return fmt.Errorf("配置的接收路径不是目录：%s", input.Directory)
		}
	}
	r.files = map[string]*observed{}
	jobs := make(chan job, r.Config.Workers*2)
	results := make(chan completed, r.Config.Workers)
	var workers sync.WaitGroup
	for i := 0; i < r.Config.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				err := r.Handler.Process(ctx, item.path, item.input)
				select {
				case results <- completed{item.path, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	defer func() { close(jobs); workers.Wait() }()
	ticker := time.NewTicker(time.Duration(r.Config.ScanSeconds * float64(time.Second)))
	defer ticker.Stop()
	r.scan(time.Now(), jobs)
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-results:
			if state := r.files[result.path]; state != nil {
				state.inFlight = false
				state.nextTry = time.Now().Add(time.Duration(r.Config.RetrySeconds * float64(time.Second)))
			}
		case now := <-ticker.C:
			r.scan(now, jobs)
		}
	}
}

func (r *Runner) scan(now time.Time, jobs chan<- job) {
	present := map[string]bool{}
	for _, input := range r.Config.Inputs {
		err := filepath.WalkDir(input.Directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.ToLower(filepath.Ext(path)) != ".bag" {
				return nil
			}
			stat, err := entry.Info()
			if err != nil {
				return err
			}
			if !stat.Mode().IsRegular() {
				return nil
			}
			present[path] = true
			state := r.files[path]
			if state == nil {
				state = &observed{size: stat.Size(), modified: stat.ModTime(), stableSince: now}
				r.files[path] = state
			}
			if state.size != stat.Size() || !state.modified.Equal(stat.ModTime()) {
				state.size = stat.Size()
				state.modified = stat.ModTime()
				state.stableSince = now
				state.nextTry = time.Time{}
			}
			if state.inFlight || now.Before(state.nextTry) || now.Sub(state.stableSince) < time.Duration(r.Config.StableSeconds*float64(time.Second)) {
				return nil
			}
			select {
			case jobs <- job{path, input}:
				state.inFlight = true
			default:
			}
			return nil
		})
		if err != nil {
			warn("扫描接收目录失败：目录=%q，原因=%v", input.Directory, err)
		}
	}
	for path, state := range r.files {
		if !present[path] && !state.inFlight {
			delete(r.files, path)
		}
	}
}
