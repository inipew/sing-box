package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log/slog"
	"mime"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	albumLimit     = 10
	captionLimit   = 1024
	maxFileSize    = 2 * 1024 * 1024 * 1024 // 2 GB
	chunkDelay     = 3 * time.Second
	uploadTimeout  = 30 * time.Minute
	stallTimeout   = 3 * time.Minute // abort chunk transfer if stalled for 3 minutes
	maxRetries     = uint64(5)
	maxConnRetries = 3
	sessionFile    = ".telegram-session"

	// upload thread limits
	defaultUploadThreads = 8
	maxUploadThreads     = 32

	// caption budget constants
	maxEnvLineBytes = 1024 * 1024
	versionMaxLen   = 120
	tagsMaxLen      = 300

	// HTML fragment lengths used in caption budget accounting.
	commitHeaderHTML = "🔨 <b>Commit:</b>\n"      // 21 runes
	cherryHeaderHTML = "🍒 <b>Cherry-pick:</b>\n" // 24 runes
	moreTagsHTML     = "\n<i>...and more tags</i>"    // 23 runes
	separatorHTML    = "\n\n"                         // 2 runes

	progressInterval = 5 * time.Second
)

// precomputed lengths derived from the HTML constants above.
var (
	commitHeaderLen = captionLength(commitHeaderHTML)
	cherryHeaderLen = captionLength(cherryHeaderHTML)
)

// ErrUploadStalled is returned when an upload chunk has zero byte transfer progress for stallTimeout.
var ErrUploadStalled = errors.New("upload stalled: connection unresponsive")

// Config holds all runtime configuration sourced from environment variables.
type Config struct {
	APIID            int
	APIHash          string
	BotToken         string
	ChatID           int64
	Version          string
	Commit           string
	CherryPickCommit string
	Tags             string
	RunURL           string
	BuiltAt          string
}

// Uploader coordinates the validation, authentication, and two-phase MTProto upload flow.
type Uploader struct {
	cfg Config
}

// NewUploader constructs a new Uploader instance.
func NewUploader(cfg Config) *Uploader {
	return &Uploader{cfg: cfg}
}

// --------------------------------------------------------------------------
// Atomic Session Storage
// --------------------------------------------------------------------------

// AtomicFileStorage implements session.Storage using atomic file operations
// to guarantee session file integrity against unexpected process termination.
type AtomicFileStorage struct {
	Path string
	mux  sync.Mutex
}

// LoadSession loads MTProto session bytes from disk. If the session file is
// missing, empty, or corrupted, it clears the corrupt file and returns
// session.ErrNotFound so that a fresh authorization takes place cleanly.
func (s *AtomicFileStorage) LoadSession(_ context.Context) ([]byte, error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		slog.Warn("failed to read session file, starting fresh", "path", s.Path, "error", err)
		_ = os.Remove(s.Path)
		_ = os.Remove(s.Path + ".tmp")
		return nil, session.ErrNotFound
	}
	if len(data) == 0 {
		slog.Warn("session file is 0 bytes (corrupted), removing and starting fresh", "path", s.Path)
		_ = os.Remove(s.Path)
		_ = os.Remove(s.Path + ".tmp")
		return nil, session.ErrNotFound
	}
	return data, nil
}

// StoreSession atomically writes session data to disk via a temporary file and atomic rename.
func (s *AtomicFileStorage) StoreSession(_ context.Context, data []byte) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	dir := filepath.Dir(s.Path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create session directory: %w", err)
		}
	}

	tmpFile := s.Path + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open temp session file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return fmt.Errorf("write session data: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return fmt.Errorf("sync session file: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("close session file: %w", err)
	}

	if err := os.Rename(tmpFile, s.Path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("atomic rename session file: %w", err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Real-time Upload Progress & Stall Detector
// --------------------------------------------------------------------------

type uploadProgress struct {
	uploaded         atomic.Int64
	total            atomic.Int64
	name             atomic.Pointer[string]
	lastProgressTime atomic.Int64 // unix nano
	lastLogged       atomic.Int64

	startTime     time.Time
	lastTickTime  time.Time
	lastTickBytes int64

	stallTimeout time.Duration
	cancelCause  context.CancelCauseFunc
}

func (p *uploadProgress) Chunk(_ context.Context, state uploader.ProgressState) error {
	if state.Total <= 0 {
		return nil
	}
	p.total.Store(state.Total)
	p.name.Store(&state.Name)

	for {
		old := p.uploaded.Load()
		if state.Uploaded <= old {
			break
		}
		if p.uploaded.CompareAndSwap(old, state.Uploaded) {
			p.lastProgressTime.Store(time.Now().UnixNano())
			break
		}
	}
	return nil
}

func (p *uploadProgress) logTick() {
	total := p.total.Load()
	if total <= 0 {
		return
	}
	uploaded := p.uploaded.Load()
	namePtr := p.name.Load()
	name := ""
	if namePtr != nil {
		name = *namePtr
	}
	pct := min100(float64(uploaded) / float64(total) * 100)
	now := time.Now()

	lastProgNano := p.lastProgressTime.Load()
	if lastProgNano > 0 && uploaded < total {
		idle := now.Sub(time.Unix(0, lastProgNano))
		if p.stallTimeout > 0 && idle >= p.stallTimeout {
			slog.Error("upload transfer stalled: no progress for "+idle.Round(time.Second).String()+", triggering reconnect retry",
				"name", name,
				"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
				"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
			)
			if p.cancelCause != nil {
				p.cancelCause(ErrUploadStalled)
			}
			return
		}
		if idle >= 30*time.Second {
			slog.Warn("  upload in progress (transfer paused / waiting for network ACK)",
				"name", name,
				"progress", fmt.Sprintf("%.1f%%", pct),
				"idle_for", idle.Round(time.Second).String(),
				"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
				"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
			)
			return
		}
	}

	deltaBytes := uploaded - p.lastTickBytes
	deltaTime := now.Sub(p.lastTickTime).Seconds()
	var currentSpeed float64
	if deltaTime > 0 && deltaBytes > 0 {
		currentSpeed = float64(deltaBytes) / deltaTime
	}

	p.lastTickTime = now
	p.lastTickBytes = uploaded
	p.lastLogged.Store(uploaded)

	speedStr := formatSpeed(currentSpeed)
	var etaStr string
	if currentSpeed > 0 && uploaded < total {
		remBytes := total - uploaded
		etaSec := float64(remBytes) / currentSpeed
		etaStr = formatETA(time.Duration(etaSec * float64(time.Second)))
	} else {
		etaStr = "--"
	}

	slog.Info("  upload progress",
		"name", name,
		"progress", fmt.Sprintf("%.1f%%", pct),
		"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
		"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
		"speed", speedStr,
		"eta", etaStr,
	)
}

func (p *uploadProgress) logFinal() {
	total := p.total.Load()
	if total <= 0 {
		return
	}
	uploaded := p.uploaded.Load()
	namePtr := p.name.Load()
	name := ""
	if namePtr != nil {
		name = *namePtr
	}
	pct := min100(float64(uploaded) / float64(total) * 100)
	elapsed := time.Since(p.startTime)
	var avgSpeed float64
	if elapsed.Seconds() > 0 {
		avgSpeed = float64(uploaded) / elapsed.Seconds()
	}

	slog.Info("  upload progress (last reported)",
		"name", name,
		"progress", fmt.Sprintf("%.1f%%", pct),
		"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
		"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
		"elapsed", elapsed.Round(time.Millisecond).String(),
		"avg_speed", formatSpeed(avgSpeed),
	)
}

func min100(v float64) float64 {
	if v > 100 {
		return 100
	}
	return v
}

func formatSpeed(bps float64) string {
	switch {
	case bps >= 1024*1024:
		return fmt.Sprintf("%.2f MB/s", bps/(1024*1024))
	case bps >= 1024:
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	case bps > 0:
		return fmt.Sprintf("%.0f B/s", bps)
	default:
		return "0 B/s"
	}
}

func formatETA(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

func (p *uploadProgress) Run(ctx context.Context, interval time.Duration) {
	p.startTime = time.Now()
	p.lastTickTime = p.startTime
	p.lastProgressTime.Store(p.startTime.UnixNano())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.logTick()
		case <-ctx.Done():
			p.logFinal()
			return
		}
	}
}

// --------------------------------------------------------------------------
// Native Git Commit Extractor
// --------------------------------------------------------------------------

// excludedCommitAuthors is a list of upstream/bot authors to filter from the commit section.
var excludedCommitAuthors = []string{
	"yelnoo", "yaotthaha", "lux5am", "inipew", "reF1nd", "Nova",
}

// parseGitLogOutput parses tab-separated git log lines into (commitLog, cherryLog).
func parseGitLogOutput(output string) (string, string) {
	var commitLines []string
	var cherryLines []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		author := strings.TrimSpace(parts[1])
		msg := strings.TrimSpace(parts[2])

		formatted := fmt.Sprintf("%s — %s", sha, msg)

		// Cherry-pick commit: authored by inipew
		if strings.EqualFold(author, "inipew") {
			if len(cherryLines) < 25 {
				cherryLines = append(cherryLines, formatted)
			}
			continue
		}

		// Main commit: filter out bot / core upstream team
		excluded := false
		authorLower := strings.ToLower(author)
		for _, ex := range excludedCommitAuthors {
			if strings.Contains(authorLower, strings.ToLower(ex)) {
				excluded = true
				break
			}
		}
		if !excluded && len(commitLines) < 25 {
			commitLines = append(commitLines, formatted)
		}
	}
	return strings.Join(commitLines, "\n"), strings.Join(cherryLines, "\n")
}

// extractGitCommits extracts commit and cherry-pick logs directly from git history.
func extractGitCommits(ctx context.Context) (string, string) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "log", "-n", "50", "--pretty=format:%h%x09%an%x09%s")
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("native git log extraction skipped", "error", err)
		return "", ""
	}
	return parseGitLogOutput(string(out))
}

// --------------------------------------------------------------------------
// Sing-box Target Architecture Recognition
// --------------------------------------------------------------------------

var knownTargets = map[string]string{
	"android-armeabi-v7a-api29": "Android ARM (API 29)",
	"android-arm64-v8a-api29":   "Android ARM64 (API 29)",
	"android-armeabi-v7a-api36": "Android ARM (API 36)",
	"android-arm64-v8a-api36":   "Android ARM64 (API 36)",
	"linux-arm64":               "Linux ARM64",
	"linux-amd64":               "Linux AMD64 (v1)",
	"linux-amd64v3":             "Linux AMD64 (v3)",
}

type targetBuildInfo struct {
	label  string
	sizeMB float64
}

func parseSingboxTarget(path string) targetBuildInfo {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	label := ""
	for targetKey, friendlyName := range knownTargets {
		if strings.HasSuffix(name, targetKey) {
			label = friendlyName
			break
		}
	}
	if label == "" {
		label = base
	}

	var sizeMB float64
	if fi, err := os.Stat(path); err == nil {
		sizeMB = float64(fi.Size()) / (1024 * 1024)
	}

	return targetBuildInfo{
		label:  label,
		sizeMB: sizeMB,
	}
}

// --------------------------------------------------------------------------
// .env loader
// --------------------------------------------------------------------------

func loadEnv(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, maxEnvLineBytes), maxEnvLineBytes)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return fmt.Errorf("%s:%d: invalid environment key %q", path, lineNo, key)
		}
		val, err = parseEnvValue(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("%s:%d: setenv %s: %w", path, lineNo, key, err)
			}
		}
	}
	return scanner.Err()
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
			// valid
		case i > 0 && c >= '0' && c <= '9':
			// valid except as first char
		default:
			return false
		}
	}
	return true
}

func parseEnvValue(val string) (string, error) {
	if val == "" {
		return "", nil
	}
	switch val[0] {
	case '"':
		if len(val) < 2 || val[len(val)-1] != '"' {
			return "", errors.New("unterminated double-quoted value")
		}
		parsed, err := strconv.Unquote(val)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return parsed, nil
	case '\'':
		if len(val) < 2 || val[len(val)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return val[1 : len(val)-1], nil
	default:
		if before, _, ok := strings.Cut(val, " #"); ok {
			val = strings.TrimSpace(before)
		}
		return val, nil
	}
}

// --------------------------------------------------------------------------
// Config loading
// --------------------------------------------------------------------------

func requireEnv(key string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("required environment variable %q is not set", key)
}

func loadConfig() (Config, error) {
	if err := loadEnv(".env"); err != nil {
		return Config{}, err
	}

	apiIDStr, err := requireEnv("API_ID")
	if err != nil {
		return Config{}, err
	}
	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil || apiID <= 0 {
		return Config{}, fmt.Errorf("API_ID must be a positive integer, got %q", apiIDStr)
	}

	apiHash, err := requireEnv("API_HASH")
	if err != nil {
		return Config{}, err
	}

	botToken, err := requireEnv("BOT_TOKEN")
	if err != nil {
		return Config{}, err
	}
	if err := validateBotToken(botToken); err != nil {
		return Config{}, err
	}

	chatIDStr, err := requireEnv("CHAT_ID")
	if err != nil {
		return Config{}, err
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil || chatID == 0 {
		return Config{}, fmt.Errorf("CHAT_ID must be a non-zero integer, got %q", chatIDStr)
	}

	commitVal := os.Getenv("COMMIT")
	cherryVal := os.Getenv("CHERRY_PICK_COMMIT")

	// If commits are not explicitly provided via environment, extract natively from git!
	if commitVal == "" || cherryVal == "" {
		c, ch := extractGitCommits(context.Background())
		if commitVal == "" {
			commitVal = c
		}
		if cherryVal == "" {
			cherryVal = ch
		}
	}

	return Config{
		APIID:            apiID,
		APIHash:          apiHash,
		BotToken:         botToken,
		ChatID:           chatID,
		Version:          os.Getenv("VERSION"),
		Commit:           commitVal,
		CherryPickCommit: cherryVal,
		Tags:             os.Getenv("TAGS"),
		RunURL:           os.Getenv("RUN_URL"),
		BuiltAt:          os.Getenv("BUILT_AT"),
	}, nil
}

func validateBotToken(token string) error {
	botID, secret, ok := strings.Cut(token, ":")
	if !ok || botID == "" || secret == "" {
		return errors.New("invalid BOT_TOKEN format: expected {id}:{secret}")
	}
	id, err := strconv.ParseInt(botID, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid BOT_TOKEN bot id %q", botID)
	}
	return nil
}

// --------------------------------------------------------------------------
// Caption & Commit formatting
// --------------------------------------------------------------------------

func captionLength(s string) int {
	return utf8.RuneCountInString(s)
}

func escapeTruncatedToLength(s string, maxLen int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || maxLen <= 0 {
		return "", false
	}

	escaped := stdhtml.EscapeString(s)
	if captionLength(escaped) <= maxLen {
		return escaped, false
	}

	runes := []rune(s)
	best := ""
	lo, hi := 0, len(runes)
	for lo <= hi {
		mid := (lo + hi) / 2
		candidate := strings.TrimSpace(string(runes[:mid])) + "..."
		esc := stdhtml.EscapeString(candidate)
		if captionLength(esc) <= maxLen {
			best = esc
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, true
}

type commitEntry struct {
	sha string
	msg string
}

func parseCommits(raw string) []commitEntry {
	var commits []commitEntry
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, maxEnvLineBytes), maxEnvLineBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sha, msg, found := strings.Cut(line, " — ")
		if !found {
			sha, msg, _ = strings.Cut(line, " ")
		}
		sha = strings.TrimSpace(sha)
		msg = strings.Trim(strings.TrimSpace(msg), " —")
		if sha != "" {
			commits = append(commits, commitEntry{sha: sha, msg: msg})
		}
	}
	return commits
}

func formatMoreCommits(n int) string {
	if n <= 1 {
		return "\n<i>...and 1 more commit</i>"
	}
	return fmt.Sprintf("\n<i>...and %d more commits</i>", n)
}

func commitLinesWithSeen(raw string, maxLen int, seen map[string]bool) (string, bool) {
	if raw == "" || maxLen <= 0 {
		return "", false
	}
	entries := parseCommits(raw)
	if len(entries) == 0 {
		return "", false
	}

	var filtered []commitEntry
	for _, e := range entries {
		if seen != nil {
			if seen[e.sha] {
				continue
			}
			seen[e.sha] = true
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		return "", false
	}

	total := len(filtered)
	var sb strings.Builder
	cumLen := 0
	shown := 0
	truncated := false

	for _, e := range filtered {
		shaEsc := stdhtml.EscapeString(e.sha)
		msgEsc := stdhtml.EscapeString(e.msg)
		prefix := "<code>" + shaEsc + "</code> — "
		fullLine := prefix + msgEsc

		sep := 0
		if shown > 0 {
			sep = 1
		}

		remainingAfterThis := total - (shown + 1)
		moreCost := 0
		if remainingAfterThis > 0 {
			moreCost = captionLength(formatMoreCommits(remainingAfterThis))
		}

		if cumLen+sep+captionLength(fullLine)+moreCost > maxLen {
			truncated = true
			if shown == 0 {
				rem := maxLen - captionLength(prefix) - moreCost
				if rem > 3 {
					if truncMsg, _ := escapeTruncatedToLength(e.msg, rem); truncMsg != "" {
						sb.WriteString(prefix + truncMsg)
						shown++
					}
				}
			}
			break
		}

		if shown > 0 {
			sb.WriteByte('\n')
			cumLen++
		}
		sb.WriteString(fullLine)
		cumLen += captionLength(fullLine)
		shown++
	}

	if shown < total {
		truncated = true
		if shown > 0 {
			rem := total - shown
			sb.WriteString(formatMoreCommits(rem))
		}
	}

	return sb.String(), truncated
}

func commitLines(raw string, maxLen int) (string, bool) {
	return commitLinesWithSeen(raw, maxLen, nil)
}

func fitCaption(parts []string) string {
	for len(parts) > 0 {
		caption := strings.Join(parts, separatorHTML)
		if captionLength(caption) <= captionLimit {
			return caption
		}
		parts = parts[:len(parts)-1]
	}
	return ""
}

// buildCaption assembles the HTML caption for the Telegram album with sing-box targets and commit budgeting.
func (u *Uploader) buildCaption(files ...string) string {
	var versionPart, filesPart, tagsPart, runURLPart, builtAtPart string

	if u.cfg.Version != "" {
		if v, _ := escapeTruncatedToLength(u.cfg.Version, versionMaxLen); v != "" {
			versionPart = "🚀 <b>Sing-box " + v + "</b>"
		}
	}

	if len(files) > 0 {
		var totalSize int64
		targets := make([]targetBuildInfo, 0, len(files))
		for _, f := range files {
			info := parseSingboxTarget(f)
			targets = append(targets, info)
			if fi, err := os.Stat(f); err == nil {
				totalSize += fi.Size()
			}
		}
		plural := "s"
		if len(files) == 1 {
			plural = ""
		}
		totalMB := float64(totalSize) / (1024 * 1024)

		if len(targets) <= 4 {
			var lines []string
			lines = append(lines, fmt.Sprintf("📦 <b>Target Builds (%d archive%s · %.1f MB):</b>", len(files), plural, totalMB))
			for _, t := range targets {
				lines = append(lines, fmt.Sprintf("• <code>%s</code> (%.1f MB)", stdhtml.EscapeString(t.label), t.sizeMB))
			}
			filesPart = strings.Join(lines, "\n")
		} else {
			filesPart = fmt.Sprintf("📦 <b>Target Builds:</b> %d archives · %.1f MB total", len(files), totalMB)
		}
	}

	if u.cfg.Tags != "" {
		if tags, trunc := escapeTruncatedToLength(u.cfg.Tags, tagsMaxLen); tags != "" {
			body := "<code>" + tags + "</code>"
			if trunc {
				body += moreTagsHTML
			}
			tagsPart = "🏷 <b>Tags:</b>\n" + body
		}
	}

	if u.cfg.RunURL != "" {
		runURLPart = "🔗 <b>Workflow Run:</b> <a href=\"" +
			stdhtml.EscapeString(u.cfg.RunURL) + "\">View Logs</a>"
	}

	builtAt := u.cfg.BuiltAt
	if builtAt == "" {
		builtAt = time.Now().UTC().Format("2006-01-02 15:04 UTC")
	}
	builtAtPart = "⏱ <b>Built:</b> <code>" + stdhtml.EscapeString(builtAt) + "</code>"

	staticParts := nonEmpty(versionPart, filesPart, runURLPart, builtAtPart)
	if tagsPart != "" {
		staticParts = append(staticParts, tagsPart)
	}
	staticLen := joinedLength(staticParts)

	hasCommit := u.cfg.Commit != ""
	hasCherry := u.cfg.CherryPickCommit != ""

	commitSectionCount := boolToInt(hasCommit) + boolToInt(hasCherry)
	totalSections := len(staticParts) + commitSectionCount
	totalSeparatorLen := separatorCount(totalSections) * captionLength(separatorHTML)
	available := captionLimit - staticLen - totalSeparatorLen

	const moreCommitsReserved = 32
	var commitBudget, cherryBudget int
	switch {
	case hasCommit && hasCherry:
		half := available / 2
		commitBudget = half - commitHeaderLen - moreCommitsReserved
		cherryBudget = (available - half) - cherryHeaderLen - moreCommitsReserved
	case hasCommit:
		commitBudget = available - commitHeaderLen - moreCommitsReserved
	case hasCherry:
		cherryBudget = available - cherryHeaderLen - moreCommitsReserved
	}
	commitBudget = max(commitBudget, 0)
	cherryBudget = max(cherryBudget, 0)

	parts := make([]string, 0, totalSections)
	if versionPart != "" {
		parts = append(parts, versionPart)
	}
	if filesPart != "" {
		parts = append(parts, filesPart)
	}

	seen := make(map[string]bool)
	// Cherry-pick commits first so unique cherry-picks are highlighted
	if hasCherry && cherryBudget > 0 {
		if body, _ := commitLinesWithSeen(u.cfg.CherryPickCommit, cherryBudget, seen); body != "" {
			parts = append(parts, cherryHeaderHTML+body)
		}
	}
	// Main commits next, omitting any duplicate SHAs already shown in cherry-pick
	if hasCommit && commitBudget > 0 {
		if body, _ := commitLinesWithSeen(u.cfg.Commit, commitBudget, seen); body != "" {
			parts = append(parts, commitHeaderHTML+body)
		}
	}

	if tagsPart != "" {
		parts = append(parts, tagsPart)
	}
	if runURLPart != "" {
		parts = append(parts, runURLPart)
	}
	if builtAtPart != "" {
		parts = append(parts, builtAtPart)
	}

	return fitCaption(parts)
}

func nonEmpty(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func joinedLength(parts []string) int {
	if len(parts) == 0 {
		return 0
	}
	total := 0
	sepLen := captionLength(separatorHTML)
	for i, p := range parts {
		total += captionLength(p)
		if i > 0 {
			total += sepLen
		}
	}
	return total
}

func separatorCount(n int) int {
	if n <= 1 {
		return 0
	}
	return n - 1
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --------------------------------------------------------------------------
// MTProto peer resolution & message deletion
// --------------------------------------------------------------------------

func resolveAccessHash(ctx context.Context, api *tg.Client, channelID int64) (int64, error) {
	res, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channelID, AccessHash: 0},
	})
	if err != nil {
		return 0, fmt.Errorf("channels.getChannels: %w", err)
	}

	var chats []tg.ChatClass
	switch r := res.(type) {
	case *tg.MessagesChats:
		chats = r.Chats
	case *tg.MessagesChatsSlice:
		chats = r.Chats
	default:
		return 0, fmt.Errorf("unexpected response type %T from channels.getChannels", res)
	}

	for _, chat := range chats {
		switch c := chat.(type) {
		case *tg.Channel:
			if c.ID == channelID {
				return c.AccessHash, nil
			}
		case *tg.ChannelForbidden:
			if c.ID == channelID {
				return c.AccessHash, nil
			}
		}
	}
	return 0, fmt.Errorf("channel %d not found — ensure the bot is a member", channelID)
}

func getPeer(ctx context.Context, api *tg.Client, chatID int64) (tg.InputPeerClass, error) {
	s := strconv.FormatInt(chatID, 10)
	switch {
	case strings.HasPrefix(s, "-100"):
		channelID, err := strconv.ParseInt(s[4:], 10, 64)
		if err != nil || channelID <= 0 {
			return nil, fmt.Errorf("malformed channel CHAT_ID %d", chatID)
		}
		hash, err := resolveAccessHash(ctx, api, channelID)
		if err != nil {
			return nil, err
		}
		return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: hash}, nil
	case chatID < 0:
		return &tg.InputPeerChat{ChatID: -chatID}, nil
	default:
		return &tg.InputPeerUser{UserID: chatID}, nil
	}
}

func getAllMessageIDs(u tg.UpdatesClass) []int {
	if u == nil {
		return nil
	}
	var (
		updates []tg.UpdateClass
		ids     []int
	)
	switch v := u.(type) {
	case *tg.UpdatesCombined:
		updates = v.Updates
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdateShortSentMessage:
		return []int{v.ID}
	default:
		return nil
	}
	for _, upd := range updates {
		switch m := upd.(type) {
		case *tg.UpdateNewMessage:
			ids = append(ids, m.Message.GetID())
		case *tg.UpdateNewChannelMessage:
			ids = append(ids, m.Message.GetID())
		}
	}
	return ids
}

func getFirstMessageID(u tg.UpdatesClass) (int, bool) {
	ids := getAllMessageIDs(u)
	if len(ids) > 0 {
		return ids[0], true
	}
	return 0, false
}

func deleteMessages(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		_, err := api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      msgIDs,
		})
		return err
	default:
		_, err := api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
			Revoke: true,
			ID:     msgIDs,
		})
		return err
	}
}

// --------------------------------------------------------------------------
// File & Checksum validation
// --------------------------------------------------------------------------

func validateZipFile(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("invalid zip archive: %w", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		return errors.New("zip archive contains 0 files")
	}
	return nil
}

func computeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func loadChecksums(dir string) map[string]string {
	checksums := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return checksums
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "SHA256SUMS") && !strings.HasSuffix(name, ".sha256") && !strings.HasSuffix(name, ".txt") {
			continue
		}
		filePath := filepath.Join(dir, name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 && len(parts[0]) == 64 {
				targetFile := filepath.Base(parts[1])
				checksums[targetFile] = strings.ToLower(parts[0])
			}
		}
	}
	return checksums
}

// validateFiles validates that paths exist, are regular non-empty files <= 2 GB,
// pass zip integrity checks, and match expected SHA256 hashes if checksum files are present.
func (u *Uploader) validateFiles(paths []string) []string {
	valid := make([]string, 0, len(paths))

	checksumCache := make(map[string]map[string]string)

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			slog.Warn("file not found, skipping", "path", p)
			continue
		}
		if !info.Mode().IsRegular() {
			slog.Warn("not a regular file, skipping", "path", p)
			continue
		}
		if info.Size() == 0 {
			slog.Warn("file is 0 bytes (empty), skipping", "path", p)
			continue
		}
		if info.Size() > maxFileSize {
			slog.Warn("file exceeds 2 GB limit, skipping", "name", info.Name(), "size_mb",
				fmt.Sprintf("%.0f", float64(info.Size())/(1024*1024)))
			continue
		}

		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".zip" {
			if err := validateZipFile(p); err != nil {
				slog.Warn("zip integrity check failed, skipping", "path", p, "error", err)
				continue
			}
		}

		dir := filepath.Dir(p)
		sums, ok := checksumCache[dir]
		if !ok {
			sums = loadChecksums(dir)
			checksumCache[dir] = sums
		}

		baseName := filepath.Base(p)
		if expectedHash, found := sums[baseName]; found {
			actualHash, err := computeSHA256(p)
			if err != nil {
				slog.Warn("could not compute sha256, skipping", "path", p, "error", err)
				continue
			}
			if !strings.EqualFold(actualHash, expectedHash) {
				slog.Error("sha256 checksum mismatch: file is corrupt, skipping",
					"file", baseName,
					"expected", expectedHash,
					"actual", actualHash,
				)
				continue
			}
			slog.Info("✓ checksum verified", "file", baseName, "sha256", actualHash[:8]+"...")
		}

		valid = append(valid, p)
	}
	return valid
}

func (u *Uploader) chunked(files []string, n int) [][]string {
	if n <= 0 || len(files) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(files)+n-1)/n)
	for i := 0; i < len(files); i += n {
		end := i + n
		if end > len(files) {
			end = len(files)
		}
		out = append(out, files[i:end])
	}
	return out
}

// --------------------------------------------------------------------------
// Retry / back-off helpers
// --------------------------------------------------------------------------

func newBackoff(ctx context.Context) backoff.BackOffContext {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Second
	bo.RandomizationFactor = 0.5
	bo.Multiplier = 2.0
	bo.MaxInterval = 30 * time.Second
	bo.MaxElapsedTime = 0
	return backoff.WithContext(backoff.WithMaxRetries(bo, maxRetries), ctx)
}

func isPermError(err error) bool {
	if err == nil {
		return false
	}
	rpcErr, ok := tgerr.As(err)
	return ok && rpcErr.Code >= 400 && rpcErr.Code < 500
}

func isConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUploadStalled) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if rpcErr, ok := tgerr.As(err); ok {
		return rpcErr.Type == "FILE_MIGRATE" || rpcErr.Code == 303
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "migrate to dc") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "upload stalled")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --------------------------------------------------------------------------
// Upload logic
// --------------------------------------------------------------------------

func (u *Uploader) getThreads() int {
	if raw := strings.TrimSpace(os.Getenv("UPLOAD_THREADS")); raw != "" {
		if t, err := strconv.Atoi(raw); err == nil && t >= 1 && t <= maxUploadThreads {
			return t
		}
		slog.Warn("invalid UPLOAD_THREADS value, using default",
			"value", raw, "default", defaultUploadThreads,
			"valid_range", fmt.Sprintf("1-%d", maxUploadThreads))
	}
	return defaultUploadThreads
}

func (u *Uploader) retryAlbum(
	ctx context.Context,
	target *message.RequestBuilder,
	opts []message.MultiMediaOption,
) (tg.UpdatesClass, error) {
	if len(opts) == 0 {
		return nil, errors.New("retryAlbum: no media options provided")
	}
	var result tg.UpdatesClass
	err := backoff.RetryNotify(
		func() error {
			if err := ctx.Err(); err != nil {
				return backoff.Permanent(err)
			}
			var e error
			result, e = target.Album(ctx, opts[0], opts[1:]...)
			if isPermError(e) {
				return backoff.Permanent(e)
			}
			return e
		},
		newBackoff(ctx),
		func(err error, d time.Duration) {
			slog.Warn("album send failed, retrying",
				"error", err,
				"backoff", d.Round(time.Millisecond),
			)
		},
	)
	return result, err
}

func (u *Uploader) uploadChunk(
	ctx context.Context,
	target *message.RequestBuilder,
	baseUp *uploader.Uploader,
	chunkIdx, lastChunkIdx int,
	chunk []string,
	caption string,
) (tg.UpdatesClass, error) {
	if len(chunk) == 0 {
		return nil, errors.New("uploadChunk: empty chunk")
	}

	chunkCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	lastFileIdx := len(chunk) - 1
	opts := make([]message.MultiMediaOption, 0, len(chunk))

	for fileIdx, path := range chunk {
		name := filepath.Base(path)
		fileStart := time.Now()

		slog.Info("  [1/3] transferring bytes",
			"file", name,
			"index", fmt.Sprintf("%d/%d", fileIdx+1, len(chunk)),
		)

		fileCtx, fileCancel := context.WithCancelCause(chunkCtx)

		prog := &uploadProgress{
			stallTimeout: stallTimeout,
			cancelCause:  fileCancel,
		}
		fileUp := baseUp.WithProgress(prog)

		progressCtx, stopProgress := context.WithCancel(fileCtx)
		go prog.Run(progressCtx, progressInterval)

		fileHandle, err := fileUp.FromPath(fileCtx, path)
		stopProgress()
		fileCancel(nil)

		if err != nil {
			if errors.Is(fileCtx.Err(), context.Canceled) && errors.Is(context.Cause(fileCtx), ErrUploadStalled) {
				return nil, fmt.Errorf("transfer %q: %w", name, ErrUploadStalled)
			}
			return nil, fmt.Errorf("transfer %q: %w", name, err)
		}

		fi, statErr := os.Stat(path)
		var fileSize int64
		if statErr == nil {
			fileSize = fi.Size()
		}
		transferElapsed := time.Since(fileStart)
		var avgSpeed float64
		if transferElapsed.Seconds() > 0 && fileSize > 0 {
			avgSpeed = float64(fileSize) / transferElapsed.Seconds()
		}

		slog.Info("  [1/3] ✓ all bytes confirmed on Telegram servers",
			"file", name,
			"size_mb", fmt.Sprintf("%.2f", float64(fileSize)/(1024*1024)),
			"elapsed", transferElapsed.Round(time.Millisecond).String(),
			"avg_speed", formatSpeed(avgSpeed),
		)

		mimeType := mime.TypeByExtension(filepath.Ext(name))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		db := message.UploadedDocument(fileHandle).
			Filename(name).
			MIME(mimeType).
			ForceFile(true)

		// --- Phase 2: server-side media registration ---
		slog.Info("  [2/3] registering media on Telegram servers", "file", name)
		media, err := target.UploadMedia(chunkCtx, db)
		if err != nil {
			return nil, fmt.Errorf("register media %q: %w", name, err)
		}
		slog.Info("  [2/3] ✓ media registered", "file", name)

		mediaDoc, ok := media.(*tg.MessageMediaDocument)
		if !ok {
			return nil, fmt.Errorf("unexpected media type for %q: %T", name, media)
		}
		doc, ok := mediaDoc.Document.(*tg.Document)
		if !ok {
			return nil, fmt.Errorf("unexpected document type for %q: %T", name, mediaDoc.Document)
		}

		inputMedia := &tg.InputMediaDocument{ID: doc.AsInput()}

		isLast := chunkIdx == lastChunkIdx && fileIdx == lastFileIdx
		var mediaOpt message.MediaOption
		if isLast {
			mediaOpt = message.Media(inputMedia, html.String(nil, caption))
		} else {
			mediaOpt = message.Media(inputMedia)
		}
		opts = append(opts, message.ForceMulti(mediaOpt))
	}

	// --- Phase 3: send album ---
	slog.Info("  [3/3] sending album to Telegram", "files", len(chunk))
	updates, err := u.retryAlbum(chunkCtx, target, opts)
	if err != nil {
		return nil, err
	}
	slog.Info("  [3/3] ✓ album sent successfully", "files", len(chunk))
	return updates, nil
}

func (u *Uploader) runUpload(
	ctx context.Context,
	client *telegram.Client,
	chunks [][]string,
	caption string,
) error {
	totalChunks := len(chunks)
	lastChunkIdx := totalChunks - 1

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			slog.Info("authenticating bot")
			if _, err := client.Auth().Bot(ctx, u.cfg.BotToken); err != nil {
				return fmt.Errorf("bot auth: %w", err)
			}
		} else {
			slog.Info("reusing existing MTProto session")
		}

		api := tg.NewClient(client)
		up := uploader.NewUploader(api).
			WithThreads(u.getThreads())

		peer, err := getPeer(ctx, api, u.cfg.ChatID)
		if err != nil {
			return fmt.Errorf("resolve peer: %w", err)
		}

		sender := message.NewSender(api)
		target := sender.To(peer)

		pinnedID, hasPinned := 0, false
		var sentMsgIDs []int

		for i, chunk := range chunks {
			if i > 0 {
				if err := sleepContext(ctx, chunkDelay); err != nil {
					return err
				}
			}
			slog.Info("uploading chunk",
				"chunk", fmt.Sprintf("%d/%d", i+1, totalChunks),
				"files", len(chunk),
			)

			updates, err := u.uploadChunk(ctx, target, up, i, lastChunkIdx, chunk, caption)
			if err != nil {
				if len(sentMsgIDs) > 0 {
					slog.Warn("partial upload failure, rolling back previously sent messages",
						"chunk", i+1,
						"sent_count", len(sentMsgIDs),
						"msg_ids", sentMsgIDs,
					)
					rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
					if delErr := deleteMessages(rollbackCtx, api, peer, sentMsgIDs); delErr != nil {
						slog.Warn("partial upload rollback failed (bot may lack delete permissions); please clean up manually",
							"msg_ids", sentMsgIDs,
							"error", delErr,
						)
					} else {
						slog.Info("✓ rollback successful: deleted partially sent messages", "count", len(sentMsgIDs))
					}
					rollbackCancel()
				}
				return fmt.Errorf("chunk %d: %w", i+1, err)
			}

			ids := getAllMessageIDs(updates)
			sentMsgIDs = append(sentMsgIDs, ids...)

			if !hasPinned {
				if len(ids) > 0 {
					pinnedID = ids[0]
					hasPinned = true
				}
			}
		}

		if hasPinned {
			slog.Info("pinning first message", "id", pinnedID)
			_, err := api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
				Silent: true,
				Peer:   peer,
				ID:     pinnedID,
			})
			if err != nil {
				slog.Warn("pin failed (non-fatal)", "error", err)
			}
		}
		return nil
	})
}

func (u *Uploader) Upload(ctx context.Context, filePaths []string) error {
	valid := u.validateFiles(filePaths)
	if len(valid) == 0 {
		return errors.New("no valid files to upload")
	}

	chunks := u.chunked(valid, albumLimit)
	caption := u.buildCaption(valid...)

	slog.Info("starting upload",
		"total_files", len(valid),
		"chunks", len(chunks),
		"version", u.cfg.Version,
		"caption_len", captionLength(caption),
	)

	client := telegram.NewClient(u.cfg.APIID, u.cfg.APIHash, telegram.Options{
		NoUpdates:      true,
		SessionStorage: &AtomicFileStorage{Path: sessionFile},
	})

	var lastErr error
	for attempt := 1; attempt <= maxConnRetries; attempt++ {
		lastErr = u.runUpload(ctx, client, chunks, caption)
		if lastErr == nil {
			slog.Info("✓ upload complete",
				"files", len(valid),
				"chunks", len(chunks),
			)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !isConnError(lastErr) {
			return lastErr
		}
		if attempt == maxConnRetries {
			break
		}
		wait := time.Duration(attempt*attempt) * 5 * time.Second
		slog.Warn("connection error, will retry",
			"attempt", fmt.Sprintf("%d/%d", attempt, maxConnRetries),
			"wait", wait,
			"error", lastErr,
		)
		if err := sleepContext(ctx, wait); err != nil {
			return err
		}
	}
	return fmt.Errorf("upload failed after %d attempts: %w", maxConnRetries, lastErr)
}

// --------------------------------------------------------------------------
// Entry point
// --------------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		slog.Error("usage: sendtotelegram <file> [file ...]")
		os.Exit(1)
	}

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	up := NewUploader(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := up.Upload(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Warn("upload cancelled by user")
			os.Exit(130)
		}
		slog.Error("upload failed", "error", err)
		os.Exit(1)
	}
}
