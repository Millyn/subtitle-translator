// Package audio captures mono PCM from a real microphone and emits speech
// segments. The device callback never blocks and never allocates.
package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
)

const (
	SampleRate       = 16000
	Channels         = 1
	BytesPerSample   = 2
	FramesPerBlock   = 480 // 30 ms at 16 kHz
	bytesPerBlock    = FramesPerBlock * BytesPerSample
	maxCallbackBytes = bytesPerBlock * 4
	callbackBuffers  = 32 // 960 ms of scheduling headroom
)

// Config controls segmentation. Durations are expressed in samples so the
// callback and processing path do not need clocks.
type Config struct {
	SilenceSamples    int
	MinVoiceSamples   int
	MaxSegmentSamples int
	PreRollSamples    int
	QueueSize         int
	VAD               VAD
}

func DefaultConfig() Config {
	defaultVAD := VAD(NewAdaptiveVAD())
	if webRTC, err := NewWebRTCVAD(2); err == nil {
		defaultVAD = webRTC
	}
	return Config{
		SilenceSamples:    SampleRate / 2,        // 500 ms
		MinVoiceSamples:   SampleRate * 3 / 10,   // 300 ms
		MaxSegmentSamples: SampleRate * 10,       // 10 s
		PreRollSamples:    SampleRate * 15 / 100, // 150 ms
		QueueSize:         8,
		VAD:               defaultVAD,
	}
}

// Stats is a lock-free snapshot of capture health.
type Stats struct {
	CallbackFrames  uint64
	CallbackDropped uint64
	SamplesCaptured uint64
	SegmentsEmitted uint64
	SegmentsDropped uint64
}

type counters struct {
	callbackFrames  atomic.Uint64
	callbackDropped atomic.Uint64
	samplesCaptured atomic.Uint64
	segmentsEmitted atomic.Uint64
	segmentsDropped atomic.Uint64
}

type audioFrame struct {
	n    int
	data [maxCallbackBytes]byte
}

type Collector struct {
	ctx *malgo.AllocatedContext
	id  malgo.DeviceID
	cfg Config

	mu       sync.Mutex
	dev      *malgo.Device
	running  bool
	started  bool
	raw      chan *audioFrame
	free     chan *audioFrame
	out      chan []byte
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	stats    counters
}

func New(ctx *malgo.AllocatedContext, id malgo.DeviceID) *Collector {
	return NewWithConfig(ctx, id, DefaultConfig())
}

func NewWithConfig(ctx *malgo.AllocatedContext, id malgo.DeviceID, cfg Config) *Collector {
	cfg = normalizedConfig(cfg)
	c := &Collector{
		ctx: ctx, id: id, cfg: cfg,
		raw:  make(chan *audioFrame, callbackBuffers),
		free: make(chan *audioFrame, callbackBuffers),
		out:  make(chan []byte, cfg.QueueSize),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	for i := 0; i < callbackBuffers; i++ {
		c.free <- &audioFrame{}
	}
	return c
}

func normalizedConfig(cfg Config) Config {
	d := DefaultConfig()
	if cfg.SilenceSamples <= 0 {
		cfg.SilenceSamples = d.SilenceSamples
	}
	if cfg.MinVoiceSamples <= 0 {
		cfg.MinVoiceSamples = d.MinVoiceSamples
	}
	if cfg.MaxSegmentSamples <= 0 {
		cfg.MaxSegmentSamples = d.MaxSegmentSamples
	}
	if cfg.MinVoiceSamples > cfg.MaxSegmentSamples {
		cfg.MinVoiceSamples = cfg.MaxSegmentSamples
	}
	if cfg.PreRollSamples < 0 {
		cfg.PreRollSamples = 0
	}
	if cfg.PreRollSamples >= cfg.MaxSegmentSamples {
		cfg.PreRollSamples = cfg.MaxSegmentSamples / 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = d.QueueSize
	}
	if cfg.VAD == nil {
		cfg.VAD = d.VAD
	}
	return cfg
}

func (c *Collector) Segments() <-chan []byte { return c.out }

// Start opens the selected native capture endpoint as 16 kHz mono signed
// 16-bit PCM. It can be called once for a Collector.
func (c *Collector) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("音频采集 context 不能为空")
	}
	if c.ctx == nil {
		return errors.New("音频系统尚未初始化")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("麦克风采集已经启动")
	}
	if c.started {
		return errors.New("此采集器已停止，重新采集请创建新的 Collector")
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = Channels
	cfg.Capture.DeviceID = c.id.Pointer()
	cfg.SampleRate = SampleRate
	cfg.PeriodSizeInFrames = FramesPerBlock

	callbacks := malgo.DeviceCallbacks{Data: c.captureCallback}
	dev, err := malgo.InitDevice(c.ctx.Context, cfg, callbacks)
	if err != nil {
		return fmt.Errorf("打开所选麦克风失败: %w", err)
	}
	c.dev = dev
	c.running = true
	c.started = true
	go c.process()
	if err := dev.Start(); err != nil {
		c.running = false
		c.stopOnce.Do(func() { close(c.stop) })
		<-c.done
		dev.Uninit()
		c.dev = nil
		return fmt.Errorf("启动麦克风采集失败: %w", err)
	}
	go func() { <-ctx.Done(); c.Stop() }()
	return nil
}

// captureCallback is deliberately bounded: obtain a preallocated frame, copy,
// enqueue. On pressure it records a drop rather than blocking the audio thread.
func (c *Collector) captureCallback(_, input []byte, frameCount uint32) {
	n := int(frameCount) * Channels * BytesPerSample
	c.stats.callbackFrames.Add(1)
	if n <= 0 || n > len(input) || n > maxCallbackBytes {
		c.stats.callbackDropped.Add(1)
		return
	}
	select {
	case frame := <-c.free:
		frame.n = n
		copy(frame.data[:n], input[:n])
		select {
		case c.raw <- frame:
			c.stats.samplesCaptured.Add(uint64(n / BytesPerSample))
		default:
			c.stats.callbackDropped.Add(1)
			c.free <- frame
		}
	default:
		c.stats.callbackDropped.Add(1)
	}
}

func (c *Collector) process() {
	defer close(c.done)
	defer close(c.out)
	segmenter := newSegmenter(c.cfg)
	for {
		select {
		case frame := <-c.raw:
			segments := segmenter.Push(frame.data[:frame.n])
			c.free <- frame
			for _, segment := range segments {
				c.emit(segment)
			}
		case <-c.stop:
			for {
				select {
				case frame := <-c.raw:
					segments := segmenter.Push(frame.data[:frame.n])
					c.free <- frame
					for _, segment := range segments {
						c.emit(segment)
					}
				default:
					if segment := segmenter.Flush(); segment != nil {
						c.emit(segment)
					}
					return
				}
			}
		}
	}
}

func (c *Collector) emit(segment []byte) {
	select {
	case c.out <- segment:
		c.stats.segmentsEmitted.Add(1)
	default:
		c.stats.segmentsDropped.Add(1)
	}
}

// Stop is idempotent and waits until queued microphone frames are processed.
func (c *Collector) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	dev := c.dev
	c.dev = nil
	c.mu.Unlock()
	if dev != nil {
		_ = dev.Stop()
		dev.Uninit()
	}
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done
}

func (c *Collector) Stats() Stats {
	return Stats{
		CallbackFrames:  c.stats.callbackFrames.Load(),
		CallbackDropped: c.stats.callbackDropped.Load(),
		SamplesCaptured: c.stats.samplesCaptured.Load(),
		SegmentsEmitted: c.stats.segmentsEmitted.Load(),
		SegmentsDropped: c.stats.segmentsDropped.Load(),
	}
}
