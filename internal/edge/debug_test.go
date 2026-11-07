package edge_test

import (
	"github.com/gotd/log"
	"github.com/gotd/log/logslog"
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("GREENGRAM_DEBUG") != "" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	os.Exit(m.Run())
}

func clientLog() log.Logger {
	if os.Getenv("GREENGRAM_DEBUG") == "" {
		return nil
	}
	return logslog.New(slog.Default())
}
