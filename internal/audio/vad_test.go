package audio

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/gen2brain/malgo"
)

type sequenceVAD struct {
	values []bool
	i      int
}

func TestCollectorErrorPathsAndDrops(t *testing.T) {
	c := NewWithConfig(nil, malgo.DeviceID{}, Config{})
	if err := c.Start(nil); err == nil {
		t.Fatal("nil context")
	}
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("nil audio system")
	}
	c.Stop() // safe before start
	c.captureCallback(nil, nil, 1)
	c.captureCallback(nil, make([]byte, maxCallbackBytes+2), uint32(maxCallbackBytes/2+1))
	for i := 0; i < callbackBuffers+1; i++ {
		c.captureCallback(nil, pcm(FramesPerBlock, 1), FramesPerBlock)
	}
	if c.Stats().CallbackDropped < 3 {
		t.Fatalf("stats: %+v", c.Stats())
	}
}

func TestNormalizedConfigAndSegmenterEdges(t *testing.T) {
	c := normalizedConfig(Config{MinVoiceSamples: 99, MaxSegmentSamples: 10, PreRollSamples: 20, QueueSize: -1})
	if c.MinVoiceSamples != 10 || c.PreRollSamples != 5 || c.QueueSize != 8 || c.VAD == nil {
		t.Fatalf("%+v", c)
	}
	s := newSegmenter(Config{VAD: &sequenceVAD{values: []bool{false, true, true}}, MinVoiceSamples: 1, MaxSegmentSamples: 3, PreRollSamples: 2})
	if got := s.Push([]byte{1}); got != nil {
		t.Fatal("odd one-byte frame")
	}
	_ = s.Push(pcm(2, 0))
	got := s.Push(pcm(5, 1000))
	if len(got) == 0 || len(got[0])/2 != 3 {
		t.Fatalf("split result: %v", got)
	}
	if flushed := s.Flush(); flushed == nil {
		t.Fatal("expected valid remainder")
	}
}

func TestAdaptiveVADEmptyAndDC(t *testing.T) {
	v := NewAdaptiveVAD()
	if v.IsSpeech(nil) {
		t.Fatal("empty")
	}
	_ = v.IsSpeech(pcm(100, 0))
	if !v.IsSpeech(pcm(100, 4000)) {
		t.Fatal("strong DC-like signal should pass guard")
	}
}

func BenchmarkAdaptiveVAD30ms(b *testing.B) {
	v, frame := NewAdaptiveVAD(), pcm(FramesPerBlock, 1200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		v.IsSpeech(frame)
	}
}

func BenchmarkCaptureCallback30ms(b *testing.B) {
	c, frame := New(nil, malgo.DeviceID{}), pcm(FramesPerBlock, 1200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.captureCallback(nil, frame, FramesPerBlock)
		f := <-c.raw
		c.free <- f
	}
}

func (v *sequenceVAD) IsSpeech([]byte) bool { value := v.values[v.i]; v.i++; return value }

func pcm(samples int, amplitude int16) []byte {
	b := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := amplitude
		if i%16 >= 8 {
			value = -value
		}
		binary.LittleEndian.PutUint16(b[i*2:], uint16(value))
	}
	return b
}

func TestAdaptiveVAD(t *testing.T) {
	vad := NewAdaptiveVAD()
	if vad.IsSpeech(pcm(FramesPerBlock, 0)) {
		t.Fatal("silence classified as speech")
	}
	if !vad.IsSpeech(pcm(FramesPerBlock, 3000)) {
		t.Fatal("clear speech-like signal was rejected")
	}
}

func TestSegmenterSilenceAndMinimum(t *testing.T) {
	// Ten voice blocks (300 ms), then 17 silent blocks (510 ms).
	values := make([]bool, 27)
	for i := 0; i < 10; i++ {
		values[i] = true
	}
	s := newSegmenter(Config{VAD: &sequenceVAD{values: values}})
	var got [][]byte
	for i, voice := range values {
		amplitude := int16(0)
		if voice {
			amplitude = 2000
		}
		got = append(got, s.Push(pcm(FramesPerBlock, amplitude))...)
		if i < len(values)-1 && len(got) != 0 {
			t.Fatalf("emitted early at block %d", i)
		}
	}
	if len(got) != 1 {
		t.Fatalf("segments = %d, want 1", len(got))
	}
	if samples := len(got[0]) / 2; samples < SampleRate*8/10 || samples > SampleRate*9/10 {
		t.Fatalf("segment samples = %d", samples)
	}
}

func TestSegmenterRejectsShortNoise(t *testing.T) {
	values := make([]bool, 21)
	for i := 0; i < 4; i++ {
		values[i] = true
	}
	s := newSegmenter(Config{VAD: &sequenceVAD{values: values}})
	for _, voice := range values {
		amplitude := int16(0)
		if voice {
			amplitude = 2000
		}
		if got := s.Push(pcm(FramesPerBlock, amplitude)); got != nil {
			t.Fatal("short noise emitted")
		}
	}
}

func TestSegmenterMaximum(t *testing.T) {
	values := make([]bool, 334) // just over ten seconds
	for i := range values {
		values[i] = true
	}
	s := newSegmenter(Config{VAD: &sequenceVAD{values: values}, PreRollSamples: 0})
	var got [][]byte
	for range values {
		got = append(got, s.Push(pcm(FramesPerBlock, 2000))...)
	}
	if len(got) != 1 {
		t.Fatalf("segments = %d, want 1", len(got))
	}
	if samples := len(got[0]) / 2; samples != SampleRate*10 {
		t.Fatalf("segment samples = %d", samples)
	}
}

func TestCaptureCallbackDoesNotAllocate(t *testing.T) {
	c := NewWithConfig(nil, malgo.DeviceID{}, DefaultConfig())
	input := pcm(FramesPerBlock, 1000)
	allocations := testing.AllocsPerRun(1000, func() {
		c.captureCallback(nil, input, FramesPerBlock)
		frame := <-c.raw
		c.free <- frame
	})
	if allocations != 0 {
		t.Fatalf("callback allocations = %.2f, want 0", allocations)
	}
	stats := c.Stats()
	if stats.CallbackFrames == 0 || stats.SamplesCaptured == 0 || stats.CallbackDropped != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
