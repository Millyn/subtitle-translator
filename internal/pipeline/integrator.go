package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type Recognizer interface{ Transcribe([]byte) (string, error) }
type Translator interface {
	Translate(context.Context, string) (string, error)
}
type Broadcaster interface{ Broadcast(string) }
type SubtitleBroadcaster interface{ BroadcastSubtitle(string, string) }

type Stats struct {
	Captured        uint64
	Recognized      uint64
	Translated      uint64
	Errors          uint64
	LastLatencyMS   int64
	LastASRMS       int64
	LastTranslateMS int64
}

type Integrator struct {
	ASR              Recognizer
	Translator       Translator
	Output           Broadcaster
	Logger           *log.Logger
	Debug            bool
	captured         atomic.Uint64
	recognized       atomic.Uint64
	translated       atomic.Uint64
	errors           atomic.Uint64
	latency          atomic.Int64
	asrLatency       atomic.Int64
	translateLatency atomic.Int64
}

type audioJob struct {
	pcm     []byte
	started time.Time
}
type textJob struct {
	text    string
	started time.Time
}

func (p *Integrator) Run(ctx context.Context, input <-chan []byte) error {
	if p.ASR == nil || p.Translator == nil || p.Output == nil {
		return fmt.Errorf("流水线组件不完整")
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
					continue
				}
				if text == "" {
					continue
				}
				p.recognized.Add(1)
				p.debugf("ASR 完成：语音 %.2fs，耗时 %v，中文=%q", float64(len(job.pcm))/32000, asrElapsed, text)
				select {
				case translationQ <- textJob{text: text, started: job.started}:
				case <-ctx.Done():
					return
				}
			}
		}
	})
	go p.safeWorker(&translationWG, func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job, ok := <-translationQ:
				if !ok {
					return
				}
				started := time.Now()
				text, err := p.Translator.Translate(ctx, job.text)
				translateElapsed := time.Since(started)
				p.translateLatency.Store(translateElapsed.Milliseconds())
				if err != nil {
					p.report(err)
					continue
				}
				if text == "" {
					continue
				}
				if rich, ok := p.Output.(SubtitleBroadcaster); ok {
					rich.BroadcastSubtitle(job.text, text)
				} else {
					p.Output.Broadcast(text)
				}
				p.translated.Add(1)
				p.latency.Store(time.Since(job.started).Milliseconds())
				p.debugf("翻译完成：耗时 %v，总耗时 %dms，英文=%q", translateElapsed, p.latency.Load(), text)
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
			p.captured.Add(1)
			p.debugf("收到断句：%d bytes，时长 %.2fs", len(pcm), float64(len(pcm))/32000)
			select {
			case asrQ <- audioJob{pcm: pcm, started: time.Now()}:
			default:
				p.report(fmt.Errorf("ASR 队列已满，丢弃最旧语音段"))
			}
		}
	}
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
	if p.Debug && p.Logger != nil {
		p.Logger.Printf("[DEBUG] "+format, args...)
	}
}
func (p *Integrator) Stats() Stats {
	return Stats{p.captured.Load(), p.recognized.Load(), p.translated.Load(), p.errors.Load(), p.latency.Load(), p.asrLatency.Load(), p.translateLatency.Load()}
}
