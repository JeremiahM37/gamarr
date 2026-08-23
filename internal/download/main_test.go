package download

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Captured before TestMain overrides them, so a test can assert what production
// actually ships rather than what the suite substitutes.
var prodImportAttempts, prodImportRetryDelay = importAttempts, importRetryDelay

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// The import retry delay is a production value measured in seconds. No test
	// should sit on it; one that exercises the retry path sets its own.
	importRetryDelay = time.Millisecond
	os.Exit(m.Run())
}
