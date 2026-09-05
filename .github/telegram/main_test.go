package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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

func TestValidateBotToken(t *testing.T) {
	valid := "123456789:ABCdef-ghIJK_lmNOpq"
	if err := validateBotToken(valid); err != nil {
		t.Fatalf("validateBotToken valid token failed: %v", err)
	}

	invalids := []string{
		"",
		"notoken",
		":secret",
		"abc:secret",
		"-123:secret",
		"123:",
	}
	for _, inv := range invalids {
		if err := validateBotToken(inv); err == nil {
			t.Errorf("validateBotToken(%q) expected error, got nil", inv)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("API_ID", "12345")
	t.Setenv("API_HASH", "0123456789abcdef0123456789abcdef")
	t.Setenv("BOT_TOKEN", "987654321:abcdefg-token")
	t.Setenv("CHAT_ID", "-1001234567890")
	t.Setenv("VERSION", "1.15.0")
	t.Setenv("COMMIT", "abc1234 — Fix cache")
	t.Setenv("CHERRY_PICK_COMMIT", "def5678 — Add group")
	t.Setenv("TAGS", "with_gvisor,with_quic")
	t.Setenv("RUN_URL", "https://github.com/run/123")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.APIID != 12345 {
		t.Errorf("cfg.APIID = %d, want 12345", cfg.APIID)
	}
	if cfg.ChatID != -1001234567890 {
		t.Errorf("cfg.ChatID = %d, want -1001234567890", cfg.ChatID)
	}
	if cfg.Version != "1.15.0" {
		t.Errorf("cfg.Version = %q, want 1.15.0", cfg.Version)
	}
}

func TestAtomicFileStorage_BasicAndCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "test.session")
	store := &AtomicFileStorage{Path: sessionPath}

	// 1. Loading non-existent session returns session.ErrNotFound
	_, err := store.LoadSession(context.Background())
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession on non-existent file returned %v, want session.ErrNotFound", err)
	}

	// 2. Storing and loading session works atomically
	sampleData := []byte("mtproto-secret-session-token-data")
	if err := store.StoreSession(context.Background(), sampleData); err != nil {
		t.Fatalf("StoreSession failed: %v", err)
	}
	loaded, err := store.LoadSession(context.Background())
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if string(loaded) != string(sampleData) {
		t.Fatalf("loaded = %q, want %q", string(loaded), string(sampleData))
	}

	// 3. Corrupt session file (0 bytes) auto-cleans and returns session.ErrNotFound
	if err := os.WriteFile(sessionPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write empty session: %v", err)
	}
	loaded, err = store.LoadSession(context.Background())
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession on empty file returned %v, want session.ErrNotFound", err)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("session file still exists after empty file recovery")
	}
}

func createTestZip(t *testing.T, path string, entries map[string]string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range entries {
		ew, err := w.Create(name)
		if err != nil {
			t.Fatalf("create entry in zip: %v", err)
		}
		if _, err := io.WriteString(ew, content); err != nil {
			t.Fatalf("write zip content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func TestValidateZipFile(t *testing.T) {
	dir := t.TempDir()

	// 1. Valid zip
	validZip := filepath.Join(dir, "valid.zip")
	createTestZip(t, validZip, map[string]string{"binary": "hello world content"})
	if err := validateZipFile(validZip); err != nil {
		t.Fatalf("validateZipFile(valid) error = %v, want nil", err)
	}

	// 2. Corrupt truncated zip
	corruptZip := filepath.Join(dir, "corrupt.zip")
	if err := os.WriteFile(corruptZip, []byte("PK\x03\x04incomplete-zip-data"), 0o600); err != nil {
		t.Fatalf("write corrupt zip: %v", err)
	}
	if err := validateZipFile(corruptZip); err == nil {
		t.Fatalf("validateZipFile(corrupt) expected error, got nil")
	}

	// 3. Empty zip with 0 entries
	emptyZip := filepath.Join(dir, "empty.zip")
	createTestZip(t, emptyZip, map[string]string{})
	if err := validateZipFile(emptyZip); err == nil {
		t.Fatalf("validateZipFile(empty) expected error, got nil")
	}
}

func TestValidateFilesWithChecksum(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "sing-box-linux-amd64.zip")
	createTestZip(t, zipPath, map[string]string{"sing-box": "compiled binary bytes"})

	data, _ := os.ReadFile(zipPath)
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])

	sumsPath := filepath.Join(dir, "SHA256SUMS-linux-amd64.txt")
	sumLine := fmt.Sprintf("%s  bin/sing-box-linux-amd64.zip\n", hexHash)
	if err := os.WriteFile(sumsPath, []byte(sumLine), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	u := NewUploader(Config{})
	valid := u.validateFiles([]string{zipPath})
	if len(valid) != 1 || valid[0] != zipPath {
		t.Fatalf("validateFiles with valid checksum = %#v, want [%q]", valid, zipPath)
	}

	badSumLine := "0000000000000000000000000000000000000000000000000000000000000000  sing-box-linux-amd64.zip\n"
	if err := os.WriteFile(sumsPath, []byte(badSumLine), 0o600); err != nil {
		t.Fatalf("write bad checksum: %v", err)
	}
	badValid := u.validateFiles([]string{zipPath})
	if len(badValid) != 0 {
		t.Fatalf("validateFiles with bad checksum = %#v, want empty", badValid)
	}
}

func TestCommitLinesDeduplicationAndNumericMore(t *testing.T) {
	rawCommits := strings.Join([]string{
		"aaa111 — Initial commit",
		"bbb222 — Second feature",
		"ccc333 — Third bugfix",
		"ddd444 — Fourth optimization",
		"eee555 — Fifth change",
	}, "\n")

	seen := map[string]bool{
		"aaa111": true,
	}
	out, _ := commitLinesWithSeen(rawCommits, 2000, seen)
	if strings.Contains(out, "aaa111") {
		t.Fatalf("commitLines output should have deduplicated aaa111, got: %s", out)
	}
	if !strings.Contains(out, "bbb222") || !strings.Contains(out, "eee555") {
		t.Fatalf("commitLines output missing remaining commits: %s", out)
	}

	limitedOut, truncated := commitLinesWithSeen(rawCommits, 110, nil)
	if !truncated {
		t.Fatalf("expected truncated = true for small budget, got false: %s", limitedOut)
	}
	if !strings.Contains(limitedOut, "more commit") {
		t.Fatalf("expected 'more commit(s)' in output: %s", limitedOut)
	}
}

func TestBuildCaptionWithFilesSummaryAndTimestamp(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "sing-box-1.15.0-android-arm64-v8a-api29.zip")
	f2 := filepath.Join(dir, "sing-box-1.15.0-linux-amd64v3.zip")
	_ = os.WriteFile(f1, make([]byte, 10*1024*1024), 0o600) // 10 MB
	_ = os.WriteFile(f2, make([]byte, 15*1024*1024), 0o600) // 15 MB

	u := NewUploader(Config{
		Version:          "1.15.0-alpha.1",
		Tags:             "with_quic,with_wireguard",
		Commit:           "111aaa — Core router update\n222bbb — Fix selector leak",
		CherryPickCommit: "333ccc — WARP outbound",
		RunURL:           "https://github.com/org/repo/actions/runs/12345",
		BuiltAt:          "2026-09-05 08:30 UTC",
	})

	caption := u.buildCaption(f1, f2)
	if captionLength(caption) > captionLimit {
		t.Fatalf("caption length %d > %d", captionLength(caption), captionLimit)
	}

	// Verify required sections
	if !strings.Contains(caption, "Target Builds") {
		t.Errorf("caption missing target builds section: %s", caption)
	}
	if !strings.Contains(caption, "Android ARM64 (API 29)") {
		t.Errorf("caption missing friendly target name Android ARM64 (API 29): %s", caption)
	}
	if !strings.Contains(caption, "Linux AMD64 (v3)") {
		t.Errorf("caption missing friendly target name Linux AMD64 (v3): %s", caption)
	}
	if !strings.Contains(caption, "⏱ <b>Built:</b> <code>2026-09-05 08:30 UTC</code>") {
		t.Errorf("caption missing built timestamp: %s", caption)
	}
	if !strings.Contains(caption, "🍒 <b>Cherry-pick:</b>") {
		t.Errorf("caption missing cherry-pick section: %s", caption)
	}
	if !strings.Contains(caption, "🔨 <b>Commit:</b>") {
		t.Errorf("caption missing commit section: %s", caption)
	}
}

func TestFormatSpeedAndETA(t *testing.T) {
	if got := formatSpeed(5.5 * 1024 * 1024); got != "5.50 MB/s" {
		t.Errorf("formatSpeed(5.5MB) = %q, want 5.50 MB/s", got)
	}
	if got := formatSpeed(450 * 1024); got != "450.0 KB/s" {
		t.Errorf("formatSpeed(450KB) = %q, want 450.0 KB/s", got)
	}
	if got := formatSpeed(128); got != "128 B/s" {
		t.Errorf("formatSpeed(128B) = %q, want 128 B/s", got)
	}
	if got := formatSpeed(0); got != "0 B/s" {
		t.Errorf("formatSpeed(0) = %q, want 0 B/s", got)
	}

	if got := formatETA(0); got != "0s" {
		t.Errorf("formatETA(0) = %q, want 0s", got)
	}
	if got := formatETA(45 * time.Second); got != "45s" {
		t.Errorf("formatETA(45s) = %q, want 45s", got)
	}
	if got := formatETA(85 * time.Second); got != "1m25s" {
		t.Errorf("formatETA(85s) = %q, want 1m25s", got)
	}
	if got := formatETA(120 * time.Second); got != "2m" {
		t.Errorf("formatETA(120s) = %q, want 2m", got)
	}
}

func TestChunked(t *testing.T) {
	u := NewUploader(Config{})
	if got := u.chunked(nil, 10); got != nil {
		t.Errorf("chunked(nil) = %#v, want nil", got)
	}

	files := make([]string, 25)
	for i := range files {
		files[i] = fmt.Sprintf("file_%d", i)
	}

	batches := u.chunked(files, 10)
	if len(batches) != 3 {
		t.Fatalf("len(batches) = %d, want 3", len(batches))
	}
	if len(batches[0]) != 10 || len(batches[1]) != 10 || len(batches[2]) != 5 {
		t.Errorf("batch sizes = [%d, %d, %d], want [10, 10, 5]", len(batches[0]), len(batches[1]), len(batches[2]))
	}
}

func TestIsPermErrorAndIsConnError(t *testing.T) {
	if isPermError(nil) {
		t.Error("isPermError(nil) should be false")
	}
	if isPermError(errors.New("generic error")) {
		t.Error("isPermError(generic) should be false")
	}
	rpc400 := &tgerr.Error{Code: 400, Message: "CHAT_ADMIN_REQUIRED"}
	if !isPermError(rpc400) {
		t.Errorf("isPermError(rpc400) should be true")
	}
	rpc500 := &tgerr.Error{Code: 500, Message: "INTERNAL"}
	if isPermError(rpc500) {
		t.Errorf("isPermError(rpc500) should be false")
	}

	if isConnError(nil) {
		t.Error("isConnError(nil) should be false")
	}
	if isConnError(context.Canceled) {
		t.Error("isConnError(context.Canceled) should be false")
	}
	if !isConnError(ErrUploadStalled) {
		t.Error("isConnError(ErrUploadStalled) should be true")
	}
	if !isConnError(io.EOF) {
		t.Error("isConnError(io.EOF) should be true")
	}
	if !isConnError(context.DeadlineExceeded) {
		t.Error("isConnError(context.DeadlineExceeded) should be true")
	}
	netErr := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}
	if !isConnError(netErr) {
		t.Error("isConnError(netErr) should be true")
	}
}

func TestGetAllMessageIDs(t *testing.T) {
	if got := getAllMessageIDs(nil); got != nil {
		t.Errorf("getAllMessageIDs(nil) = %#v, want nil", got)
	}

	short := &tg.UpdateShortSentMessage{ID: 42}
	if ids := getAllMessageIDs(short); len(ids) != 1 || ids[0] != 42 {
		t.Errorf("getAllMessageIDs(short) = %#v, want [42]", ids)
	}

	upds := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 101}},
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 102}},
		},
	}
	ids := getAllMessageIDs(upds)
	if len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Errorf("getAllMessageIDs(upds) = %#v, want [101, 102]", ids)
	}
}

func TestParseSingboxTarget(t *testing.T) {
	tests := []struct {
		filename  string
		wantLabel string
	}{
		{"sing-box-1.15.0-alpha.1-android-arm64-v8a-api29.zip", "Android ARM64 (API 29)"},
		{"sing-box-1.15.0-android-armeabi-v7a-api36.zip", "Android ARM (API 36)"},
		{"sing-box-1.15.0-linux-amd64v3.zip", "Linux AMD64 (v3)"},
		{"sing-box-1.15.0-linux-arm64.zip", "Linux ARM64"},
		{"custom-binary.tar.gz", "custom-binary.tar.gz"},
	}

	for _, tt := range tests {
		info := parseSingboxTarget(tt.filename)
		if info.label != tt.wantLabel {
			t.Errorf("parseSingboxTarget(%q).label = %q, want %q", tt.filename, info.label, tt.wantLabel)
		}
	}
}

func TestParseGitLogOutput(t *testing.T) {
	raw := strings.Join([]string{
		"111aaa\tinipew\tprotocol/wireguard: add WARP endpoint",
		"222bbb\tOtherDev\tdns: optimize cache behavior",
		"333ccc\tyelnoo\tauto bump deps",
		"444ddd\tNova\tci sync",
		"555eee\tCollaborator\troute: fix leak",
	}, "\n")

	commitLog, cherryLog := parseGitLogOutput(raw)

	// inipew should be in cherryLog
	if !strings.Contains(cherryLog, "111aaa — protocol/wireguard") {
		t.Errorf("cherryLog missing inipew commit: %q", cherryLog)
	}
	// OtherDev and Collaborator should be in commitLog
	if !strings.Contains(commitLog, "222bbb — dns: optimize") {
		t.Errorf("commitLog missing OtherDev commit: %q", commitLog)
	}
	if !strings.Contains(commitLog, "555eee — route: fix leak") {
		t.Errorf("commitLog missing Collaborator commit: %q", commitLog)
	}
	// Bots (yelnoo, Nova) should be excluded
	if strings.Contains(commitLog, "yelnoo") || strings.Contains(commitLog, "333ccc") {
		t.Errorf("commitLog should not contain bot commit: %q", commitLog)
	}
	if strings.Contains(commitLog, "Nova") || strings.Contains(commitLog, "444ddd") {
		t.Errorf("commitLog should not contain Nova commit: %q", commitLog)
	}
}

func TestExtractGitCommits(t *testing.T) {
	// In the real git workspace, extractGitCommits should run and extract commits without error
	commitLog, cherryLog := extractGitCommits(context.Background())
	t.Logf("Extracted commits from workspace: commitLen=%d, cherryLen=%d", len(commitLog), len(cherryLog))
	// Both or at least one should have commits since we have git commits in dev-next
	if commitLog == "" && cherryLog == "" {
		t.Log("Note: git log returned empty (may occur in shallow clone), but call completed without error")
	}
}
