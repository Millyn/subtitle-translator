package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"subtitle-translator/internal/translate"
)

type Recognizer interface{ Transcribe([]byte) (string, error) }
type Translator interface {
	Translate(context.Context, string) (string, error)
}
type Broadcaster interface{ Broadcast(string) }
type SubtitleBroadcaster interface{ BroadcastSubtitle(string, string) }
type DetailedSubtitleBroadcaster interface{ BroadcastSubtitleDetail(string, string, string) }
type RichTranslator interface {
	TranslateRich(context.Context, translate.RichRequest) (translate.RichResult, error)
	LastUsage() translate.Usage
}

type DebugEvent struct {
	SegmentID       uint64
	Timestamp       time.Time
	DurationMS      int64
	SegmentReason   string
	ASRModel        string
	Raw             string
	Corrected       string
	English         string
	Diff            string
	Profile         string
	MatchedTerms    []string
	Context         []string
	ASRMS           int64
	TranslateMS     int64
	TotalMS         int64
	PromptTokens    int
	CacheHitTokens  int
	CacheMissTokens int
	OutputTokens    int
	TotalTokens     int
	Attempts        int
	Error           string
	RequestBody     string
	ResponseBody    string
}

type Stats struct {
	Captured        uint64
	Recognized      uint64
	Translated      uint64
	Errors          uint64
	LastLatencyMS   int64
	LastASRMS       int64
	LastTranslateMS int64
	Corrected       uint64
	PromptTokens    uint64
	CacheHitTokens  uint64
	OutputTokens    uint64
}

type Integrator struct {
	ASR              Recognizer
	Translator       Translator
	Output           Broadcaster
	Logger           *log.Logger
	DebugFunc        func() bool
	ASRModel         string
	MaxSegmentSecond int
	// BuildRichRequest is evaluated immediately before each API call, so
	// control-page changes take effect without restarting the program.
	BuildRichRequest func(source string, correctedHistory []string) translate.RichRequest
	DebugSink        func(DebugEvent)

	captured         atomic.Uint64
	recognized       atomic.Uint64
	translated       atomic.Uint64
	errors           atomic.Uint64
	latency          atomic.Int64
	asrLatency       atomic.Int64
	translateLatency atomic.Int64
	corrected        atomic.Uint64
	promptTokens     atomic.Uint64
	cacheHitTokens   atomic.Uint64
	outputTokens     atomic.Uint64
}

type audioJob struct {
	pcm     []byte
	started time.Time
	id      uint64
}
type textJob struct {
	text       string
	started    time.Time
	id         uint64
	durationMS int64
	reason     string
	asrMS      int64
}

func (p *Integrator) Run(ctx context.Context, input <-chan []byte) error {
	if p.ASR == nil || p.Translator == nil || p.Output == nil {
		return fmt.Errorf("pipeline components are incomplete")
	}
	asrQ := make(chan audioJob, 2)
	translationQ := make(chan textJob, 2)
	var asrWG, translationWG sync.WaitGroup
	asrWG.Add(1)
	translationWG.Add(1)
	go p.safeWorker(&asrWG, func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job, ok := <-asrQ:
				if !ok {
					return
				}
				started := time.Now()
				text, err := p.ASR.Transcribe(job.pcm)
				asrElapsed := time.Since(started)
				p.asrLatency.Store(asrElapsed.Milliseconds())
				if err != nil {
					p.report(err)
					p.emitDebug(DebugEvent{SegmentID: job.id, Timestamp: time.Now(), DurationMS: int64(len(job.pcm)) * 1000 / 32000, SegmentReason: p.segmentReason(len(job.pcm)), ASRModel: p.ASRModel, ASRMS: asrElapsed.Milliseconds(), TotalMS: time.Since(job.started).Milliseconds(), Error: err.Error()})
					continue
				}
				if text == "" {
					continue
				}
				p.recognized.Add(1)
				p.debugf("ASR complete: id=%d audio=%.2fs elapsed=%v raw=%q", job.id, float64(len(job.pcm))/32000, asrElapsed, text)
				select {
				case translationQ <- textJob{text: text, started: job.started, id: job.id, durationMS: int64(len(job.pcm)) * 1000 / 32000, reason: p.segmentReason(len(job.pcm)), asrMS: asrElapsed.Milliseconds()}:
				case <-ctx.Done():
					return
				}
			}
		}
	})
	go p.safeWorker(&translationWG, func() {
		var correctedHistory []string
		for {
			select {
			case <-ctx.Done():
				return
			case job, ok := <-translationQ:
				if !ok {
					return
				}
				started := time.Now()
				raw, corrected := job.text, job.text
				var english string
				var result translate.RichResult
				var usage translate.Usage
				var request translate.RichRequest
				var err error
				if rich, ok := p.Translator.(RichTranslator); ok && p.BuildRichRequest != nil {
					request = p.BuildRichRequest(raw, append([]string(nil), correctedHistory...))
					result, err = rich.TranslateRich(ctx, request)
					usage = rich.LastUsage()
					corrected, english = result.CorrectedChinese, result.English
				} else {
					english, err = p.Translator.Translate(ctx, raw)
				}
				translateElapsed := time.Since(started)
				p.translateLatency.Store(translateElapsed.Milliseconds())
				if err != nil {
					p.report(err)
					p.broadcastDetail(raw, raw, "")
					p.emitDebug(DebugEvent{SegmentID: job.id, Timestamp: time.Now(), DurationMS: job.durationMS, SegmentReason: job.reason, ASRModel: p.ASRModel, Raw: raw, Corrected: raw, Profile: request.ActiveProfile, Context: request.RecentContext, ASRMS: job.asrMS, TranslateMS: translateElapsed.Milliseconds(), TotalMS: time.Since(job.started).Milliseconds(), Attempts: result.Attempts, Error: err.Error(), RequestBody: result.RequestBody, ResponseBody: result.ResponseBody})
					continue
				}
				if english == "" {
					continue
				}
				if corrected == "" {
					corrected = raw
				}
				p.broadcastDetail(raw, corrected, english)
				p.translated.Add(1)
				p.latency.Store(time.Since(job.started).Milliseconds())
				if corrected != raw {
					p.corrected.Add(1)
				}
				p.promptTokens.Add(nonNegative(usage.PromptTokens))
				p.cacheHitTokens.Add(nonNegative(usage.CacheHitTokens))
				p.outputTokens.Add(nonNegative(usage.OutputTokens))
				correctedHistory = append(correctedHistory, corrected)
				if len(correctedHistory) > 5 {
					correctedHistory = correctedHistory[len(correctedHistory)-5:]
				}
				diff := ""
				if corrected != raw {
					diff = raw + " → " + corrected
				}
				p.emitDebug(DebugEvent{SegmentID: job.id, Timestamp: time.Now(), DurationMS: job.durationMS, SegmentReason: job.reason, ASRModel: p.ASRModel, Raw: raw, Corrected: corrected, English: english, Diff: diff, Profile: request.ActiveProfile, MatchedTerms: result.MatchedTerms, Context: request.RecentContext, ASRMS: job.asrMS, TranslateMS: translateElapsed.Milliseconds(), TotalMS: p.latency.Load(), PromptTokens: usage.PromptTokens, CacheHitTokens: usage.CacheHitTokens, CacheMissTokens: usage.CacheMissTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, Attempts: result.Attempts, RequestBody: result.RequestBody, ResponseBody: result.ResponseBody})
				p.debugf("translation complete: id=%d corrected=%q english=%q elapsed=%v total=%dms profile=%s terms=%v tokens=%d cache_hit=%d attempts=%d", job.id, corrected, english, translateElapsed, p.latency.Load(), request.ActiveProfile, result.MatchedTerms, usage.TotalTokens, usage.CacheHitTokens, result.Attempts)
			}
		}
	})
	for {
		select {
		case <-ctx.Done():
			close(asrQ)
			asrWG.Wait()
			close(translationQ)
			translationWG.Wait()
			return nil
		case pcm, ok := <-input:
			if !ok {
				close(asrQ)
				asrWG.Wait()
				close(translationQ)
				translationWG.Wait()
				return nil
			}
			id := p.captured.Add(1)
			p.debugf("segment received: id=%d bytes=%d duration=%.2fs reason=%s", id, len(pcm), float64(len(pcm))/32000, p.segmentReason(len(pcm)))
			select {
			case asrQ <- audioJob{pcm: pcm, started: time.Now(), id: id}:
			default:
				p.report(fmt.Errorf("ASR queue is full; dropped newest segment"))
			}
		}
	}
}

func nonNegative(v int) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func (p *Integrator) safeWorker(wg *sync.WaitGroup, fn func()) {
	defer wg.Done()
	defer func() {
		if v := recover(); v != nil {
			p.report(fmt.Errorf("worker panic: %v", v))
		}
	}()
	fn()
}
func (p *Integrator) report(err error) {
	p.errors.Add(1)
	if p.Logger != nil {
		p.Logger.Print(err)
	}
}
func (p *Integrator) debugf(format string, args ...any) {
	enabled := p.DebugFunc != nil && p.DebugFunc()
	if enabled && p.Logger != nil {
		p.Logger.Printf("[DEBUG] "+format, args...)
	}
}
func (p *Integrator) broadcastDetail(raw, corrected, english string) {
	if detailed, ok := p.Output.(DetailedSubtitleBroadcaster); ok {
		detailed.BroadcastSubtitleDetail(raw, corrected, english)
	} else if rich, ok := p.Output.(SubtitleBroadcaster); ok {
		rich.BroadcastSubtitle(corrected, english)
	} else {
		p.Output.Broadcast(english)
	}
}
func (p *Integrator) emitDebug(event DebugEvent) {
	if p.DebugSink != nil && (p.DebugFunc == nil || p.DebugFunc()) {
		p.DebugSink(event)
	}
}
func (p *Integrator) segmentReason(bytes int) string {
	if p.MaxSegmentSecond > 0 && bytes >= p.MaxSegmentSecond*32000-960 {
		return "max_duration"
	}
	return "silence"
}
func (p *Integrator) Stats() Stats {
	return Stats{Captured: p.captured.Load(), Recognized: p.recognized.Load(), Translated: p.translated.Load(), Errors: p.errors.Load(), LastLatencyMS: p.latency.Load(), LastASRMS: p.asrLatency.Load(), LastTranslateMS: p.translateLatency.Load(), Corrected: p.corrected.Load(), PromptTokens: p.promptTokens.Load(), CacheHitTokens: p.cacheHitTokens.Load(), OutputTokens: p.outputTokens.Load()}
}
