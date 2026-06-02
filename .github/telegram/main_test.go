package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadEnvParsesAndPreservesExisting(t *testing.T) {
	t.Setenv("PLAIN", "")
	t.Setenv("DOUBLE", "")
	t.Setenv("SINGLE", "")
	t.Setenv("EXISTING", "keep")

	path := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"# ignored",
		"PLAIN=value # trailing comment",
		`DOUBLE="quoted\nvalue"`,
		"export SINGLE='literal value'",
		"EXISTING=replace",
		"NO_EQUALS",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	if err := loadEnv(path); err != nil {
		t.Fatalf("loadEnv returned error: %v", err)
	}

	if got := os.Getenv("PLAIN"); got != "value" {
		t.Fatalf("PLAIN = %q, want value", got)
	}
	if got := os.Getenv("DOUBLE"); got != "quoted\nvalue" {
		t.Fatalf("DOUBLE = %q, want quoted newline value", got)
	}
	if got := os.Getenv("SINGLE"); got != "literal value" {
		t.Fatalf("SINGLE = %q, want literal value", got)
	}
	if got := os.Getenv("EXISTING"); got != "keep" {
		t.Fatalf("EXISTING = %q, want keep", got)
	}
}

func TestLoadEnvRejectsMalformedQuotedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(`BAD="unterminated`), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	err := loadEnv(path)
	if err == nil {
		t.Fatal("loadEnv returned nil error for malformed value")
	}
	if !strings.Contains(err.Error(), "unterminated double-quoted value") {
		t.Fatalf("error = %q, want unterminated quote error", err)
	}
}

func TestBuildCaptionEscapesAndFits(t *testing.T) {
	longTags := strings.Repeat("<tag&>", 80)
	longMessage := strings.Repeat("fix <thing> & more ", 80)
	u := NewUploader(Config{
		Version:          `1.0 <beta> & "rc"`,
		Tags:             longTags,
		Commit:           "abc123 - " + longMessage,
		CherryPickCommit: "def456 - " + longMessage,
	})

	caption := u.buildCaption()
	if caption == "" {
		t.Fatal("caption is empty")
	}
	if got := captionLength(caption); got > captionLimit {
		t.Fatalf("caption length = %d, want <= %d\n%s", got, captionLimit, caption)
	}
	if strings.Contains(caption, "1.0 <beta>") {
		t.Fatalf("caption contains unescaped version: %s", caption)
	}
	if !strings.Contains(caption, "1.0 &lt;beta&gt; &amp;") {
		t.Fatalf("caption does not contain escaped version: %s", caption)
	}
	if strings.Contains(caption, "<tag&>") {
		t.Fatalf("caption contains unescaped tags: %s", caption)
	}
}

func TestCommitLinesTruncatesLongFirstLineSafely(t *testing.T) {
	out, truncated := commitLines("abc123 - "+strings.Repeat("<unsafe>&", 40), 80)
	if !truncated {
		t.Fatal("commitLines did not report truncation")
	}
	if out == "" {
		t.Fatal("commitLines returned empty output for long first line")
	}
	if got := captionLength(out); got > 80 {
		t.Fatalf("commit line length = %d, want <= 80: %s", got, out)
	}
	if strings.Contains(out, "<unsafe>") {
		t.Fatalf("commit line contains unescaped message: %s", out)
	}
	if !strings.Contains(out, "<code>abc123</code>") || !strings.Contains(out, "...") {
		t.Fatalf("commit line missing sha or truncation marker: %s", out)
	}
}

func TestValidateFilesSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(file, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	u := NewUploader(Config{})
	got := u.validateFiles([]string{file, dir, filepath.Join(dir, "missing")})
	if len(got) != 1 || got[0] != file {
		t.Fatalf("validateFiles = %#v, want only %q", got, file)
	}
}

func TestGetThreadsUsesValidatedEnv(t *testing.T) {
	u := NewUploader(Config{})

	t.Setenv("UPLOAD_THREADS", "16")
	if got := u.getThreads(); got != 16 {
		t.Fatalf("threads = %d, want 16", got)
	}

	t.Setenv("UPLOAD_THREADS", "99")
	if got := u.getThreads(); got != 8 {
		t.Fatalf("threads = %d, want default 8 for out-of-range value", got)
	}

	t.Setenv("UPLOAD_THREADS", " 4 ")
	if got := u.getThreads(); got != 4 {
		t.Fatalf("threads = %d, want trimmed value 4", got)
	}
}

func TestSleepContextReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepContext(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepContext error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("sleepContext took %s after cancellation", elapsed)
	}
}
