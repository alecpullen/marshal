package acp

import (
	"context"
	"io"
)

func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}
