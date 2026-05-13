package torznab

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

// Silence slog during test runs.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
