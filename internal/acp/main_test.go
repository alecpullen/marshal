package acp

import (
	"os"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
