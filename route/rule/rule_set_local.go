package rule

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/fswatch"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service/filemanager"

	"go4.org/netipx"
)

var _ adapter.RuleSet = (*LocalRuleSet)(nil)

type LocalRuleSet struct {
	ctx          context.Context
	logger       logger.Logger
	tag          string
	path         string
	content      atomic.Pointer[ruleSetContent]
	fileFormat   string
	watcher      *fswatch.Watcher
	updateAccess sync.Mutex
	access       sync.Mutex
	callbacks    list.List[adapter.RuleSetUpdateCallback]
	refs         atomic.Int32
	ruleSetType  string
}

func NewLocalRuleSet(ctx context.Context, logger logger.Logger, tag string, options option.RuleSet) (*LocalRuleSet, error) {
	ruleSet := &LocalRuleSet{
		ctx:         ctx,
		logger:      logger,
		tag:         tag,
		fileFormat:  options.Format,
		ruleSetType: options.Type,
	}
	ruleSet.content.Store(&ruleSetContent{})
	if options.Type == C.RuleSetTypeInline {
		if len(options.InlineOptions.Rules) == 0 {
			return nil, E.New("empty inline rule-set")
		}
		err := ruleSet.reloadRules(options.InlineOptions.Rules)
		if err != nil {
			return nil, err
		}
	} else {
		filePath := filemanager.BasePath(ctx, strings.ReplaceAll(options.LocalOptions.Path, C.RuleSetTagPlaceholder, tag))
		filePath, _ = filepath.Abs(filePath)
		ruleSet.path = filePath
		err := ruleSet.reloadFile(filePath)
		if err != nil {
			return nil, err
		}
		watcher, err := fswatch.NewWatcher(fswatch.Options{
			Path: []string{filePath},
			Callback: func(path string) {
				ruleSet.updateAccess.Lock()
				defer ruleSet.updateAccess.Unlock()
				uErr := ruleSet.reloadFile(path)
				if uErr != nil {
					logger.Error(E.Cause(uErr, "reload rule-set ", tag))
				}
			},
		})
		if err != nil {
			return nil, err
		}
		ruleSet.watcher = watcher
	}
	return ruleSet, nil
}

func (s *LocalRuleSet) Name() string {
	return s.tag
}

func (s *LocalRuleSet) ProviderInfo() adapter.RuleSetProviderInfo {
	content := s.content.Load()
	var (
		ruleCount uint64
		updatedAt time.Time
	)
	if content != nil {
		ruleCount = content.ruleCount
		updatedAt = content.lastUpdated
	}
	return adapter.RuleSetProviderInfo{
		Type:      s.ruleSetType,
		Format:    s.fileFormat,
		RuleCount: ruleCount,
		UpdatedAt: updatedAt,
	}
}

func (s *LocalRuleSet) Update(ctx context.Context) error {
	if s.ruleSetType == C.RuleSetTypeInline || s.path == "" {
		return nil
	}
	s.updateAccess.Lock()
	defer s.updateAccess.Unlock()
	return s.reloadFileContext(ctx, s.path)
}

func (s *LocalRuleSet) String() string {
	return s.content.Load().String()
}

func (s *LocalRuleSet) StartContext(ctx context.Context, startContext *adapter.HTTPStartContext) error {
	if s.watcher != nil {
		err := s.watcher.Start()
		if err != nil {
			s.logger.Error(E.Cause(err, "watch rule-set file"))
		}
	}
	return nil
}

func (s *LocalRuleSet) reloadFile(path string) error {
	return s.reloadFileContext(s.ctx, path)
}

func (s *LocalRuleSet) reloadFileContext(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fileInfo, err := filemanager.Stat(ctx, path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var ruleSet option.PlainRuleSetCompat
	switch s.fileFormat {
	case C.RuleSetFormatSource, "":
		content, err := filemanager.ReadFile(ctx, path)
		if err != nil {
			return err
		}
		ruleSet, err = json.UnmarshalExtended[option.PlainRuleSetCompat](content)
		if err != nil {
			return err
		}
	case C.RuleSetFormatBinary:
		setFile, err := filemanager.Open(ctx, path)
		if err != nil {
			return err
		}
		defer setFile.Close()
		ruleSet, err = srs.Read(setFile, false)
		if err != nil {
			return err
		}
	default:
		return E.New("unknown rule-set format: ", s.fileFormat)
	}
	ruleSet, err := mmapRuleSet(ctx, s.logger, s.tag, ruleSet)
	if err != nil {
		return err
	}
	plainRuleSet, err := ruleSet.Upgrade()
	if err != nil {
		return err
	}
	return s.reloadRulesAtContext(ctx, plainRuleSet.Rules, fileInfo.ModTime())
}

func (s *LocalRuleSet) reloadRules(headlessRules []option.HeadlessRule) error {
	return s.reloadRulesAtContext(s.ctx, headlessRules, time.Time{})
}

func (s *LocalRuleSet) reloadRulesAt(headlessRules []option.HeadlessRule, updatedAt time.Time) error {
	return s.reloadRulesAtContext(s.ctx, headlessRules, updatedAt)
}

func (s *LocalRuleSet) reloadRulesAtContext(ctx context.Context, headlessRules []option.HeadlessRule, updatedAt time.Time) error {
	rules := make([]adapter.HeadlessRule, len(headlessRules))
	var err error
	for i, ruleOptions := range headlessRules {
		rules[i], err = NewHeadlessRule(ctx, ruleOptions)
		if err != nil {
			return E.Cause(err, "parse rule_set.rules.[", i, "]")
		}
	}
	metadata := buildRuleSetMetadata(headlessRules)
	err = validateRuleSetMetadataUpdate(ctx, s.tag, metadata)
	if err != nil {
		return err
	}
	s.content.Store(&ruleSetContent{
		rules:       rules,
		metadata:    metadata,
		ruleCount:   uint64(len(headlessRules)),
		lastUpdated: updatedAt,
	})
	s.access.Lock()
	callbacks := s.callbacks.Array()
	s.access.Unlock()
	for _, callback := range callbacks {
		callback(s)
	}
	return nil
}

func (s *LocalRuleSet) Metadata() adapter.RuleSetMetadata {
	return s.content.Load().metadata
}

func (s *LocalRuleSet) ExtractIPSet() []*netipx.IPSet {
	return common.FlatMap(s.content.Load().rules, extractIPSetFromRule)
}

func (s *LocalRuleSet) IncRef() {
	s.refs.Add(1)
}

func (s *LocalRuleSet) DecRef() {
	if s.refs.Add(-1) < 0 {
		panic("rule-set: negative refs")
	}
}

func (s *LocalRuleSet) Cleanup() {
	if s.refs.Load() == 0 {
		current := s.content.Load()
		if current != nil && (current.ruleCount > 0 || !current.lastUpdated.IsZero()) {
			s.content.Store(&ruleSetContent{
				ruleCount:   current.ruleCount,
				lastUpdated: current.lastUpdated,
			})
		} else {
			s.content.Store(&ruleSetContent{})
		}
	}
}

func (s *LocalRuleSet) RegisterCallback(callback adapter.RuleSetUpdateCallback) *list.Element[adapter.RuleSetUpdateCallback] {
	s.access.Lock()
	defer s.access.Unlock()
	return s.callbacks.PushBack(callback)
}

func (s *LocalRuleSet) UnregisterCallback(element *list.Element[adapter.RuleSetUpdateCallback]) {
	s.access.Lock()
	defer s.access.Unlock()
	s.callbacks.Remove(element)
}

func (s *LocalRuleSet) Close() error {
	s.content.Store(&ruleSetContent{})
	return common.Close(common.PtrOrNil(s.watcher))
}

func (s *LocalRuleSet) Match(metadata *adapter.InboundContext) bool {
	return s.content.Load().Match(metadata)
}

func (s *LocalRuleSet) loadContent() *ruleSetContent {
	return s.content.Load()
}
