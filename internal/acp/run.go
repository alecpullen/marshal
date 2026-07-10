package acp

import (
	"context"
	"encoding/json"
	"io"
)

func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	srv := NewServer(stdin, stdout)
	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{
			"protocolVersion": 1,
			"agent":           map[string]any{"name": "Marshal"},
		}, nil
	})
	return srv.Serve(ctx)
}
