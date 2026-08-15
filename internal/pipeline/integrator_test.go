package pipeline

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"
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
