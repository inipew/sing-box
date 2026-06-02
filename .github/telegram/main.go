package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log/slog"
	"mime"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	// These mirror the literal strings used in buildCaption and commitLines.
	commitHeaderHTML = "🔨 <b>Commit:</b>\n"           // 21 runes
	cherryHeaderHTML = "🍒 <b>Cherry-pick:</b>\n"      // 24 runes
	moreCommitsHTML  = "\n<i>...and more commits</i>" // 27 runes
	moreTagsHTML     = "\n<i>...and more tags</i>"    // 23 runes
	separatorHTML    = "\n\n"                         // 2 runes
)

// precomputed lengths derived from the HTML constants above.
var (
	commitHeaderLen = captionLength(commitHeaderHTML)
	cherryHeaderLen = captionLength(cherryHeaderHTML)
	moreCommitsLen  = captionLength(moreCommitsHTML)
)

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
}

// Uploader coordinates the validation, authentication, and two-phase MTProto upload flow.
type Uploader struct {
	cfg Config
}

// NewUploader constructs a new Uploader instance.
func NewUploader(cfg Config) *Uploader {
	return &Uploader{cfg: cfg}
}

// uploadProgress collects file-transfer state from uploader threads with
// lock-free atomic operations, then emits periodic log lines from a single
// goroutine started by Run.
//
// Design notes:
//   - Chunk() uses a CAS loop so that concurrent threads cannot regress
//     the uploaded counter backwards (each thread may report its own partial
//     progress, not a global cumulative value).
//   - The ticker goroutine detects when callbacks have stalled and logs a
//     heartbeat so CI output is never silent even on slow connections.
//   - A guaranteed final line is emitted when the context is cancelled so
//     the log always terminates with an accurate end-state entry.
type uploadProgress struct {
	uploaded    atomic.Int64
	total       atomic.Int64
	name        atomic.Pointer[string]
	lastLogged  atomic.Int64 // last uploaded value that was printed
}

// Chunk is called concurrently by uploader threads. It stores state using a
// max-CAS loop so the monotonically-highest uploaded value is always kept,
// regardless of which thread writes last.
func (p *uploadProgress) Chunk(_ context.Context, state uploader.ProgressState) error {
	if state.Total <= 0 {
		return nil
	}
	p.total.Store(state.Total)
	p.name.Store(&state.Name)
	// Keep the maximum seen value — threads may report per-thread progress
	// rather than a global cumulative, so values can arrive out of order.
	for {
		old := p.uploaded.Load()
		if state.Uploaded <= old {
			break // already have a higher (or equal) value
		}
		if p.uploaded.CompareAndSwap(old, state.Uploaded) {
			break
		}
	}
	return nil
}

// logTick is called by the ticker goroutine. When progress hasn't changed
// since the last call it logs a "reporting paused" heartbeat so the user
// knows the upload is still running despite the stalled percentage.
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

	lastPrinted := p.lastLogged.Load()
	p.lastLogged.Store(uploaded)

	if uploaded == lastPrinted && uploaded > 0 {
		// Callbacks have stalled — emit heartbeat so CI never goes silent.
		slog.Info("  upload in progress (callback reporting paused — still uploading)",
			"name", name,
			"last_known", fmt.Sprintf("%.1f%%", pct),
			"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
			"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
		)
		return
	}
	slog.Info("  upload progress",
		"name", name,
		"progress", fmt.Sprintf("%.1f%%", pct),
		"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
		"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
	)
}

// logFinal is called once when the upload goroutine completes. It always
// prints the last known state regardless of whether progress changed.
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
	slog.Info("  upload progress (last reported)",
		"name", name,
		"progress", fmt.Sprintf("%.1f%%", pct),
		"uploaded_mb", fmt.Sprintf("%.2f", float64(uploaded)/(1024*1024)),
		"total_mb", fmt.Sprintf("%.2f", float64(total)/(1024*1024)),
	)
}

func min100(v float64) float64 {
	if v > 100 {
		return 100
	}
	return v
}

// Run emits a progress line every interval until ctx is cancelled, then
// emits one final line. Call this in a dedicated goroutine and cancel ctx
// immediately after the upload returns.
func (p *uploadProgress) Run(ctx context.Context, interval time.Duration) {
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
// .env loader
// --------------------------------------------------------------------------

// loadEnv reads key=value pairs from a .env file and populates the environment.
// It supports export-prefixed lines, single-quoted, and double-quoted values.
// Already-set variables are not overwritten (same semantics as dotenv libraries).
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
			// always valid
		case i > 0 && c >= '0' && c <= '9':
			// valid except as first character
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
		// Strip inline comments (# preceded by space).
		if before, _, ok := strings.Cut(val, " #"); ok {
			val = strings.TrimSpace(before)
		}
		return val, nil
	}
}

// --------------------------------------------------------------------------
// Config loading
// --------------------------------------------------------------------------

// requireEnv returns the trimmed value of key or an error if unset/empty.
func requireEnv(key string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("required environment variable %q is not set", key)
}

// loadConfig assembles Config from environment variables (with .env fallback).
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

	return Config{
		APIID:            apiID,
		APIHash:          apiHash,
		BotToken:         botToken,
		ChatID:           chatID,
		Version:          os.Getenv("VERSION"),
		Commit:           os.Getenv("COMMIT"),
		CherryPickCommit: os.Getenv("CHERRY_PICK_COMMIT"),
		Tags:             os.Getenv("TAGS"),
		RunURL:           os.Getenv("RUN_URL"),
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
// Caption helpers
// --------------------------------------------------------------------------

// captionLength returns the Unicode rune count of s.
// Telegram counts caption characters by code point, not byte.
func captionLength(s string) int {
	return utf8.RuneCountInString(s)
}

// escapeTruncatedToLength HTML-escapes s and truncates it to fit within maxLen
// runes. Returns the (possibly truncated) escaped string and whether truncation
// occurred. Returns ("", false) when s is empty or maxLen ≤ 0.
func escapeTruncatedToLength(s string, maxLen int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || maxLen <= 0 {
		return "", false
	}

	escaped := stdhtml.EscapeString(s)
	if captionLength(escaped) <= maxLen {
		return escaped, false
	}

	// Binary-search over source runes to find the largest prefix whose
	// escaped form (with "..." appended) fits within maxLen.
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

// commitLines formats a raw multi-line commit log into HTML, escaping entities
// and truncating to stay within maxLen runes. Returns the formatted string and
// whether it was truncated.
//
// Each input line is expected to be in the form:
//
//	<sha> — <message>   or   <sha> <message>
//
// The SHA is rendered inside <code> tags; the message is plain-escaped text.
func commitLines(raw string, maxLen int) (string, bool) {
	if raw == "" || maxLen <= 0 {
		return "", false
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, maxEnvLineBytes), maxEnvLineBytes)

	cumLen := 0
	truncated := false
	first := true

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		sha, msg, found := strings.Cut(line, " — ")
		if !found {
			sha, msg, _ = strings.Cut(line, " ")
		}
		sha = stdhtml.EscapeString(strings.TrimSpace(sha))
		msgTrimmed := strings.Trim(msg, " —")
		msgEsc := stdhtml.EscapeString(msgTrimmed)

		prefix := "<code>" + sha + "</code> — "
		fullLine := prefix + msgEsc

		// Account for the "\n" separator between lines.
		sep := 0
		if !first {
			sep = 1
		}

		if cumLen+sep+captionLength(fullLine) > maxLen {
			truncated = true
			// Attempt to fit at least a truncated first line.
			if first {
				remaining := maxLen - captionLength(prefix)
				if remaining > 3 { // room for at least "..."
					if truncMsg, _ := escapeTruncatedToLength(msgTrimmed, remaining); truncMsg != "" {
						sb.WriteString(prefix + truncMsg)
					}
				}
			}
			break
		}

		if !first {
			sb.WriteByte('\n')
			cumLen++
		}
		sb.WriteString(fullLine)
		cumLen += captionLength(fullLine)
		first = false
	}

	if err := scanner.Err(); err != nil {
		truncated = true
	}
	return sb.String(), truncated
}

// fitCaption joins parts with double newlines, dropping trailing parts until
// the joined result fits within captionLimit. An empty string is returned only
// when every individual part exceeds the limit.
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

// buildCaption assembles the HTML caption for the Telegram album.
// It budgets the 1 024-rune caption limit carefully so that commit sections
// always receive the correct share of remaining space.
func (u *Uploader) buildCaption() string {
	// --- Static parts ---
	var versionPart, tagsPart, runURLPart string

	if u.cfg.Version != "" {
		if v, _ := escapeTruncatedToLength(u.cfg.Version, versionMaxLen); v != "" {
			versionPart = "🚀 <b>Sing-box " + v + "</b>"
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

	// Measure fixed-part consumption.
	staticParts := nonEmpty(versionPart, tagsPart, runURLPart)
	staticLen := joinedLength(staticParts)

	hasCommit := u.cfg.Commit != ""
	hasCherry := u.cfg.CherryPickCommit != ""

	// Calculate the budget available for commit sections, accounting for the
	// separators that will appear between static parts and commit sections.
	commitSectionCount := boolToInt(hasCommit) + boolToInt(hasCherry)
	totalSections := len(staticParts) + commitSectionCount
	totalSeparatorLen := separatorCount(totalSections) * captionLength(separatorHTML)
	available := captionLimit - staticLen - totalSeparatorLen

	// Split the remaining budget between the commit sections.
	var commitBudget, cherryBudget int
	switch {
	case hasCommit && hasCherry:
		half := available / 2
		commitBudget = half - commitHeaderLen - moreCommitsLen
		cherryBudget = (available - half) - cherryHeaderLen - moreCommitsLen
	case hasCommit:
		commitBudget = available - commitHeaderLen - moreCommitsLen
	case hasCherry:
		cherryBudget = available - cherryHeaderLen - moreCommitsLen
	}
	// Clamp to zero; negative budgets mean the static content already fills
	// the caption and commit sections will be omitted by fitCaption.
	commitBudget = max(commitBudget, 0)
	cherryBudget = max(cherryBudget, 0)

	// --- Assemble all parts ---
	parts := make([]string, 0, len(staticParts)+commitSectionCount)
	parts = append(parts, staticParts...)

	if hasCommit && commitBudget > 0 {
		if body, trunc := commitLines(u.cfg.Commit, commitBudget); body != "" {
			if trunc {
				body += moreCommitsHTML
			}
			parts = append(parts, commitHeaderHTML+body)
		}
	}
	if hasCherry && cherryBudget > 0 {
		if body, trunc := commitLines(u.cfg.CherryPickCommit, cherryBudget); body != "" {
			if trunc {
				body += moreCommitsHTML
			}
			parts = append(parts, cherryHeaderHTML+body)
		}
	}

	return fitCaption(parts)
}

// nonEmpty returns a new slice containing only the non-empty strings from ss.
func nonEmpty(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// joinedLength returns the rune count of strings joined by separatorHTML.
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

// separatorCount returns the number of separators between n sections.
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
// MTProto peer resolution
// --------------------------------------------------------------------------

// resolveAccessHash fetches the access_hash for a channel/supergroup.
// Bots cannot call messages.getDialogs, but channels.getChannels accepts
// AccessHash=0 when the bot is already a member.
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

// getPeer builds the correct InputPeer for an MTProto API call.
// Channel/supergroup IDs (prefixed with -100) require a resolved access_hash.
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

// getFirstMessageID extracts the first message ID from any UpdatesClass variant.
func getFirstMessageID(u tg.UpdatesClass) (int, bool) {
	if u == nil {
		return 0, false
	}
	var updates []tg.UpdateClass
	switch v := u.(type) {
	case *tg.UpdatesCombined:
		updates = v.Updates
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdateShortSentMessage:
		return v.ID, true
	default:
		return 0, false
	}
	for _, upd := range updates {
		switch m := upd.(type) {
		case *tg.UpdateNewMessage:
			return m.Message.GetID(), true
		case *tg.UpdateNewChannelMessage:
			return m.Message.GetID(), true
		}
	}
	return 0, false
}

// --------------------------------------------------------------------------
// File helpers
// --------------------------------------------------------------------------

// validateFiles returns only paths that exist, are regular files, and are
// within the 2 GB Telegram size limit.
func (u *Uploader) validateFiles(paths []string) []string {
	valid := make([]string, 0, len(paths))
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
		if info.Size() > maxFileSize {
			slog.Warn("file exceeds 2 GB limit, skipping", "name", info.Name(), "size_mb",
				fmt.Sprintf("%.0f", float64(info.Size())/(1024*1024)))
			continue
		}
		valid = append(valid, p)
	}
	return valid
}

// chunked splits files into batches of at most n elements.
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

// newBackoff returns a context-bound exponential back-off capped at maxRetries.
// Initial interval 2 s, doubles up to 30 s, with ±50 % jitter.
func newBackoff(ctx context.Context) backoff.BackOffContext {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Second
	bo.RandomizationFactor = 0.5
	bo.Multiplier = 2.0
	bo.MaxInterval = 30 * time.Second
	bo.MaxElapsedTime = 0 // stopped by maxRetries, not elapsed time
	// Wrap context first so cancellation propagates even inside a sleep.
	return backoff.WithContext(backoff.WithMaxRetries(bo, maxRetries), ctx)
}

// isPermError returns true for RPC errors that will never succeed on retry
// (4xx range), so the caller should fail immediately.
func isPermError(err error) bool {
	if err == nil {
		return false
	}
	rpcErr, ok := tgerr.As(err)
	return ok && rpcErr.Code >= 400 && rpcErr.Code < 500
}

// isConnError returns true for low-level network errors that warrant a full
// TCP reconnect. FLOOD_WAIT is intentionally excluded: gotd handles it
// internally. context.Canceled is excluded because it is user-initiated.
func isConnError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
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
		// DC migration requires reconnecting to a different DC.
		return rpcErr.Type == "FILE_MIGRATE" || rpcErr.Code == 303
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "migrate to dc") ||
		strings.Contains(lower, "connection refused")
}

// sleepContext blocks for d or until ctx is cancelled, whichever comes first.
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

// getThreads returns the number of concurrent MTProto upload threads.
// Defaults to defaultUploadThreads; overridable via UPLOAD_THREADS (1–32).
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

// retryAlbum sends a Telegram album with exponential back-off on transient
// errors. Permanent 4xx errors fail immediately.
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

// progressInterval is how often the ticker goroutine emits a progress line.
const progressInterval = 5 * time.Second

// uploadChunk uploads files in chunk to Telegram and sends them as an album.
// The upload is broken into three phases, each with its own log line so that
// CI output is never silent for more than progressInterval seconds:
//
//  1. Raw bytes transfer  — up.FromPath, progress ticker goroutine active.
//  2. Media registration  — target.UploadMedia, logged per-file.
//  3. Album send          — retryAlbum, logged once per chunk.
func (u *Uploader) uploadChunk(
	ctx context.Context,
	target *message.RequestBuilder,
	up *uploader.Uploader,
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

		// --- Phase 1: raw byte transfer ---
		slog.Info("  [1/3] transferring bytes",
			"file", name,
			"index", fmt.Sprintf("%d/%d", fileIdx+1, len(chunk)),
		)

		prog := &uploadProgress{}
		up = up.WithProgress(prog)

		// Start the ticker reporter; cancel it as soon as FromPath returns.
		progressCtx, stopProgress := context.WithCancel(chunkCtx)
		go prog.Run(progressCtx, progressInterval)

		fileHandle, err := up.FromPath(chunkCtx, path)
		stopProgress() // stops ticker and emits the guaranteed final line

		if err != nil {
			return nil, fmt.Errorf("transfer %q: %w", name, err)
		}

		// up.FromPath only returns nil after ALL parts are acknowledged by
		// Telegram's servers. Log the real file size (from disk) so there
		// is zero ambiguity — the full file is on Telegram regardless of
		// what the progress callbacks reported.
		if fi, statErr := os.Stat(path); statErr == nil {
			slog.Info("  [1/3] all bytes confirmed on Telegram servers",
				"file", name,
				"size_mb", fmt.Sprintf("%.2f", float64(fi.Size())/(1024*1024)),
			)
		}

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
		slog.Info("  [2/3] media registered", "file", name)

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
	slog.Info("  [3/3] album sent", "files", len(chunk))
	return updates, nil
}

// runUpload performs one complete upload pass using a provided Telegram client.
// The client is passed in (not created here) so that the authenticated MTProto
// session is reused across connection-level retries, avoiding repeated bot.Auth()
// calls and the FLOOD_WAIT penalties they trigger.
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
		// The uploader is created once and reused across all chunks and files.
		// Progress tracking is set per-file inside uploadChunk, so no global
		// WithProgress is needed here.
		up := uploader.NewUploader(api).
			WithThreads(u.getThreads())

		peer, err := getPeer(ctx, api, u.cfg.ChatID)
		if err != nil {
			return fmt.Errorf("resolve peer: %w", err)
		}

		sender := message.NewSender(api)
		target := sender.To(peer)

		pinnedID, hasPinned := 0, false

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
				return fmt.Errorf("chunk %d: %w", i+1, err)
			}

			if !hasPinned {
				if id, ok := getFirstMessageID(updates); ok {
					pinnedID = id
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

// Upload validates input paths and performs two-phase uploads with DC-migration
// and connection-level retries.
//
// A single Telegram client is created for the entire call and reused across all
// retry attempts. Session state is persisted to sessionFile so subsequent
// process runs skip the bot.Auth() round-trip entirely.
func (u *Uploader) Upload(ctx context.Context, filePaths []string) error {
	valid := u.validateFiles(filePaths)
	if len(valid) == 0 {
		return errors.New("no valid files to upload")
	}

	chunks := u.chunked(valid, albumLimit)
	caption := u.buildCaption()

	slog.Info("starting upload",
		"total_files", len(valid),
		"chunks", len(chunks),
		"version", u.cfg.Version,
		"caption_len", captionLength(caption),
	)

	client := telegram.NewClient(u.cfg.APIID, u.cfg.APIHash, telegram.Options{
		NoUpdates:      true,
		SessionStorage: &session.FileStorage{Path: sessionFile},
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
			return lastErr // permanent or logic error — do not retry
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
			os.Exit(130) // 128 + SIGINT
		}
		slog.Error("upload failed", "error", err)
		os.Exit(1)
	}
}
