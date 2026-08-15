package audio

import (
	"encoding/binary"

	webrtcvad "github.com/maxhawkins/go-webrtcvad"
)

// VAD classifies one PCM S16LE frame. Implementations are called by the
// processing goroutine, never by the real-time device callback.
type VAD interface{ IsSpeech(pcm []byte) bool }

// WebRTCVAD wraps Google's WebRTC VAD. Processing happens on the background
// audio goroutine and never in the native microphone callback.
type WebRTCVAD struct {
	inner    *webrtcvad.VAD
	fallback *AdaptiveVAD
}

func NewWebRTCVAD(mode int) (*WebRTCVAD, error) {
	v, err := webrtcvad.New()
	if err != nil {
		return nil, err
	}
	if err = v.SetMode(mode); err != nil {
		return nil, err
	}
	return &WebRTCVAD{inner: v, fallback: NewAdaptiveVAD()}, nil
}

func (v *WebRTCVAD) IsSpeech(pcm []byte) bool {
	const frameBytes = FramesPerBlock * BytesPerSample
	if v == nil || v.inner == nil {
		return false
	}
	for len(pcm) >= frameBytes {
		active, err := v.inner.Process(SampleRate, pcm[:frameBytes])
		if err == nil && active {
			return true
		}
		if err != nil {
			return v.fallback.IsSpeech(pcm)
		}
		pcm = pcm[frameBytes:]
	}
	if len(pcm) == 320 || len(pcm) == 640 {
		active, err := v.inner.Process(SampleRate, pcm)
		return err == nil && active
	}
	return false
}

// AdaptiveVAD is a small pure-Go VAD designed for low game-host overhead. It
// combines an adaptive noise floor, mean absolute energy and a zero-crossing
// guard. A WebRTC/sherpa VAD can be injected through Config.VAD without
// changing capture code.
type AdaptiveVAD struct {
	noiseFloor  int64
	initialized bool
}

func NewAdaptiveVAD() *AdaptiveVAD { return &AdaptiveVAD{noiseFloor: 180} }

func (v *AdaptiveVAD) IsSpeech(pcm []byte) bool {
	samples := len(pcm) / 2
	if samples == 0 {
		return false
	}
	var sum int64
	var crossings int
	var previous int16
	for i := 0; i < samples; i++ {
		sample := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		value := int64(sample)
		if value < 0 {
			value = -value
		}
		sum += value
		if i > 0 && (sample < 0) != (previous < 0) {
			crossings++
		}
		previous = sample
	}
	energy := sum / int64(samples)
	if !v.initialized {
		v.noiseFloor = energy
		if v.noiseFloor < 120 {
			v.noiseFloor = 120
		}
		v.initialized = true
	}
	threshold := v.noiseFloor * 5 / 2
	if threshold < 320 {
		threshold = 320
	}
	// Reject isolated DC/handling bumps, but do not reject voiced sounds with
	// naturally low zero-crossing rates.
	speech := energy >= threshold && (crossings > 0 || energy >= threshold*2)
	if !speech {
		// Slow EWMA follows fans/game noise without immediately learning speech.
		v.noiseFloor = (v.noiseFloor*63 + energy) / 64
		if v.noiseFloor < 120 {
			v.noiseFloor = 120
		}
	}
	return speech
}

type segmenter struct {
	cfg            Config
	pre            []byte
	segment        []byte
	active         bool
	silenceSamples int
	voiceSamples   int
}

func newSegmenter(cfg Config) *segmenter { return &segmenter{cfg: normalizedConfig(cfg)} }

func (s *segmenter) Push(frame []byte) [][]byte {
	if len(frame) < 2 {
		return nil
	}
	if len(frame)%2 != 0 {
		frame = frame[:len(frame)-1]
	}
	samples := len(frame) / 2
	voice := s.cfg.VAD.IsSpeech(frame)
	return s.pushClassified(frame, samples, voice)
}

func (s *segmenter) pushClassified(frame []byte, samples int, voice bool) [][]byte {
	if !s.active {
		if !voice {
			s.appendPreRoll(frame)
			return nil
		}
		s.active = true
		s.segment = append(s.segment, s.pre...)
		s.pre = s.pre[:0]
	}

	// A device callback block can straddle the ten-second boundary. Split it
	// rather than emitting an out-of-contract (>10 s) segment.
	maxBytes := s.cfg.MaxSegmentSamples * BytesPerSample
	remainingBytes := maxBytes - len(s.segment)
	if remainingBytes < len(frame) {
		if remainingBytes > 0 {
			s.appendActive(frame[:remainingBytes], remainingBytes/BytesPerSample, voice)
		}
		out := s.finish(true)
		rest := frame[remainingBytes:]
		return append(out, s.pushClassified(rest, len(rest)/BytesPerSample, voice)...)
	}
	s.appendActive(frame, samples, voice)

	if len(s.segment) >= maxBytes {
		return s.finish(true)
	}
	if s.silenceSamples >= s.cfg.SilenceSamples {
		return s.finish(false)
	}
	return nil
}

func (s *segmenter) appendActive(frame []byte, samples int, voice bool) {
	s.segment = append(s.segment, frame...)
	if voice {
		s.voiceSamples += samples
		s.silenceSamples = 0
	} else {
		s.silenceSamples += samples
	}
}

func (s *segmenter) appendPreRoll(frame []byte) {
	maxBytes := s.cfg.PreRollSamples * BytesPerSample
	if maxBytes <= 0 {
		return
	}
	s.pre = append(s.pre, frame...)
	if excess := len(s.pre) - maxBytes; excess > 0 {
		copy(s.pre, s.pre[excess:])
		s.pre = s.pre[:maxBytes]
	}
}

func (s *segmenter) finish(maxLength bool) [][]byte {
	valid := s.voiceSamples >= s.cfg.MinVoiceSamples
	segment := s.segment
	if !maxLength && s.silenceSamples > 0 {
		// Keep at most the configured trailing silence.
		maxTrailing := s.cfg.SilenceSamples * BytesPerSample
		if s.silenceSamples*BytesPerSample > maxTrailing {
			segment = segment[:len(segment)-(s.silenceSamples*BytesPerSample-maxTrailing)]
		}
	}
	s.segment = nil
	s.active = false
	s.silenceSamples = 0
	s.voiceSamples = 0
	if valid {
		return [][]byte{segment}
	}
	return nil
}

func (s *segmenter) Flush() []byte {
	segments := s.finish(false)
	if len(segments) == 1 {
		return segments[0]
	}
	return nil
}
