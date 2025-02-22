package dockerutil

import (
	"context"
	"io"
	"sync"

	"github.com/docker/buildx/util/progress"
	"github.com/docker/cli/cli/command"
	"github.com/docker/docker/client"
)

// Client represents an active docker object.
type Client struct {
	cli command.Cli
}

// NewClient initializes a new docker client.
func NewClient(cli command.Cli) *Client {
	return &Client{cli: cli}
}

// API returns a new docker API client.
func (c *Client) API(name string) (client.APIClient, error) {
	if name == "" {
		name = c.cli.CurrentContext()
	}
	return NewClientAPI(c.cli, name)
}

// LoadImage imports an image to docker.
func (c *Client) LoadImage(ctx context.Context, name string, status progress.Writer) (io.WriteCloser, func(), error) {
	dapi, err := c.API(name)
	if err != nil {
		return nil, nil, err
	}

	pr, pw := io.Pipe()
	done := make(chan struct{})

	ctx, cancel := context.WithCancel(ctx)
	var w *waitingWriter
	w = &waitingWriter{
		PipeWriter: pw,
		f: func() {
			resp, err := dapi.ImageLoad(ctx, pr, false)
			defer close(done)
			if err != nil {
				pr.CloseWithError(err)
				w.mu.Lock()
				w.err = err
				w.mu.Unlock()
				return
			}
			prog := progress.WithPrefix(status, "", false)
			progress.FromReader(prog, "importing to docker", resp.Body)
		},
		done:   done,
		cancel: cancel,
	}
	return w, func() {
		pr.Close()
	}, nil
}

func (c *Client) Features(ctx context.Context, name string) map[Feature]bool {
	features := make(map[Feature]bool)
	if dapi, err := c.API(name); err == nil {
		if info, err := dapi.Info(ctx); err == nil {
			for _, v := range info.DriverStatus {
				switch v[0] {
				case "driver-type":
					if v[1] == "io.containerd.snapshotter.v1" {
						features[OCIImporter] = true
					}
				}
			}
		}
	}
	return features
}

type waitingWriter struct {
	*io.PipeWriter
	f      func()
	once   sync.Once
	mu     sync.Mutex
	err    error
	done   chan struct{}
	cancel func()
}

func (w *waitingWriter) Write(dt []byte) (int, error) {
	w.once.Do(func() {
		go w.f()
	})
	return w.PipeWriter.Write(dt)
}

func (w *waitingWriter) Close() error {
	err := w.PipeWriter.Close()
	<-w.done
	if err == nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.err
	}
	return err
}
