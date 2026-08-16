package pipeline

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"subtitle-translator/internal/translate"
)

type fakeASR struct{}

func (fakeASR) Transcribe([]byte) (string, error) { return "你好", nil }

type fakeTranslator struct{}

func (fakeTranslator) Translate(context.Context, string) (string, error) { return "Hello", nil }

type fakeOutput struct {
	mu  sync.Mutex
	got []string
}
type richOutput struct{ source, translation string }

func (r *richOutput) Broadcast(string) {}
func (r *richOutput) BroadcastSubtitle(source, translation string) {
	r.source, r.translation = source, translation
}

type detailedOutput struct{ raw, corrected, english string }

func (d *detailedOutput) Broadcast(string) {}
func (d *detailedOutput) BroadcastSubtitleDetail(raw, corrected, english string) {
	d.raw, d.corrected, d.english = raw, corrected, english
}

type fakeRichTranslator struct{ fail bool }

func (f *fakeRichTranslator) Translate(context.Context, string) (string, error) { return "legacy", nil }
func (f *fakeRichTranslator) TranslateRich(context.Context, translate.RichRequest) (translate.RichResult, error) {
	if f.fail {
		return translate.RichResult{}, errors.New("api failed")
	}
	return translate.RichResult{CorrectedChinese: "这个确实不错", English: "This is good.", WasCorrected: true, MatchedTerms: []string{"术语"}, Attempts: 1}, nil
}
func (f *fakeRichTranslator) LastUsage() translate.Usage {
	return translate.Usage{PromptTokens: 20, CacheHitTokens: 10, OutputTokens: 5, TotalTokens: 25}
}

type funcASR func([]byte) (string, error)

func (f funcASR) Transcribe(b []byte) (string, error) { return f(b) }

type funcTranslator func(context.Context, string) (string, error)

func (f funcTranslator) Translate(c context.Context, s string) (string, error) { return f(c, s) }

func TestRunValidationAndClosedInput(t *testing.T) {
	if err := (&Integrator{}).Run(context.Background(), make(chan []byte)); err == nil {
		t.Fatal("missing components")
	}
	in := make(chan []byte)
	close(in)
	p := &Integrator{ASR: fakeASR{}, Translator: fakeTranslator{}, Output: &fakeOutput{}}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestRunASRTranslatorErrorsAndEmpty(t *testing.T) {
	tests := []struct {
		name           string
		asr            funcASR
		tr             funcTranslator
		wantRecognized uint64
	}{
		{"asr error", funcASR(func([]byte) (string, error) { return "", errors.New("asr") }), funcTranslator(func(context.Context, string) (string, error) { return "x", nil }), 0},
		{"asr empty", funcASR(func([]byte) (string, error) { return "", nil }), funcTranslator(func(context.Context, string) (string, error) { return "x", nil }), 0},
		{"translate error", funcASR(func([]byte) (string, error) { return "x", nil }), funcTranslator(func(context.Context, string) (string, error) { return "", errors.New("tr") }), 1},
		{"translate empty", funcASR(func([]byte) (string, error) { return "x", nil }), funcTranslator(func(context.Context, string) (string, error) { return "", nil }), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make(chan []byte, 1)
			in <- []byte{1}
			close(in)
			p := &Integrator{ASR: tt.asr, Translator: tt.tr, Output: &fakeOutput{}, Logger: log.New(io.Discard, "", 0)}
			if err := p.Run(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			st := p.Stats()
			if st.Captured != 1 || st.Recognized != tt.wantRecognized {
				t.Fatalf("%+v", st)
			}
		})
	}
}

func TestSafeWorkerRecovers(t *testing.T) {
	p := &Integrator{Logger: log.New(io.Discard, "", 0)}
	var wg sync.WaitGroup
	wg.Add(1)
	p.safeWorker(&wg, func() { panic("boom") })
	wg.Wait()
	if p.Stats().Errors != 1 {
		t.Fatalf("%+v", p.Stats())
	}
}

func BenchmarkPipelineSingleSegment(b *testing.B) {
	for i := 0; i < b.N; i++ {
		in := make(chan []byte, 1)
		in <- []byte{1}
		close(in)
		p := &Integrator{ASR: fakeASR{}, Translator: fakeTranslator{}, Output: &fakeOutput{}}
		_ = p.Run(context.Background(), in)
	}
}

func (o *fakeOutput) Broadcast(s string) { o.mu.Lock(); o.got = append(o.got, s); o.mu.Unlock() }
func TestRun(t *testing.T) {
	in := make(chan []byte, 1)
	out := &fakeOutput{}
	p := &Integrator{ASR: fakeASR{}, Translator: fakeTranslator{}, Output: out}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- p.Run(ctx, in) }()
	in <- []byte{1}
	deadline := time.Now().Add(time.Second)
	for p.Stats().Translated < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if p.Stats().Translated != 1 {
		t.Fatal("pipeline did not broadcast")
	}
}

func TestBilingualBroadcastAndDebug(t *testing.T) {
	in := make(chan []byte, 1)
	in <- make([]byte, 32000)
	close(in)
	out := &richOutput{}
	p := &Integrator{ASR: fakeASR{}, Translator: fakeTranslator{}, Output: out, Logger: log.New(io.Discard, "", 0), Debug: true}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if out.source != "你好" || out.translation != "Hello" {
		t.Fatalf("%q %q", out.source, out.translation)
	}
	s := p.Stats()
	if s.LastASRMS < 0 || s.LastTranslateMS < 0 || s.LastLatencyMS < 0 {
		t.Fatalf("%+v", s)
	}
}

func TestRichCorrectionContextAndDebug(t *testing.T) {
	in := make(chan []byte, 2)
	in <- make([]byte, 32000)
	in <- make([]byte, 32000)
	close(in)
	out := &detailedOutput{}
	var events []DebugEvent
	p := &Integrator{
		ASR: fakeASR{}, Translator: &fakeRichTranslator{}, Output: out, MaxSegmentSecond: 10,
		BuildRichRequest: func(source string, history []string) translate.RichRequest {
			return translate.RichRequest{Source: source, RecentContext: history, ActiveProfile: "iracing"}
		},
		DebugSink: func(e DebugEvent) { events = append(events, e) },
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if out.corrected != "这个确实不错" || out.english != "This is good." || len(events) != 2 || len(events[1].Context) != 1 {
		t.Fatalf("output=%+v events=%+v", out, events)
	}
	s := p.Stats()
	if s.Corrected != 2 || s.PromptTokens != 40 || s.CacheHitTokens != 20 || s.OutputTokens != 10 {
		t.Fatalf("stats=%+v", s)
	}
}

func TestRichFailureStillBroadcastsRawChinese(t *testing.T) {
	in := make(chan []byte, 1)
	in <- make([]byte, 32000)
	close(in)
	out := &detailedOutput{}
	var event DebugEvent
	p := &Integrator{ASR: fakeASR{}, Translator: &fakeRichTranslator{fail: true}, Output: out,
		BuildRichRequest: func(source string, history []string) translate.RichRequest {
			return translate.RichRequest{Source: source}
		},
		DebugSink: func(e DebugEvent) { event = e },
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if out.raw == "" || out.corrected != out.raw || out.english != "" || event.Error == "" {
		t.Fatalf("output=%+v event=%+v", out, event)
	}
}
