package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
	"subtitle-translator/internal/asr"
	"subtitle-translator/internal/audio"
	"subtitle-translator/internal/config"
	"subtitle-translator/internal/device"
	"subtitle-translator/internal/glossary"
	"subtitle-translator/internal/liveconfig"
	"subtitle-translator/internal/model"
	"subtitle-translator/internal/pipeline"
	"subtitle-translator/internal/platform"
	"subtitle-translator/internal/translate"
	wsserver "subtitle-translator/internal/ws"
	webpage "subtitle-translator/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Printf("错误：%v", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.json", "配置文件路径")
	listDevices := flag.Bool("list-devices", false, "仅列出真实麦克风设备")
	check := flag.Bool("check", false, "检查配置、模型和运行库")
	deviceOverride := flag.Int("device", -2, "麦克风编号（覆盖配置）")
	modelOverride := flag.String("model", "", "模型 ID（覆盖配置）")
	micTest := flag.Duration("mic-test", 0, "打开真实麦克风并采集指定时长，例如 3s")
	asrBenchmark := flag.Duration("asr-benchmark", 0, "使用真实麦克风录制一次并对比所有已安装 ASR 模型，例如 15s")
	debugOverride := flag.Bool("debug", false, "启用详细调试日志（覆盖配置）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *debugOverride {
		cfg.Debug = true
	}
	if cfg.Debug && cfg.DebugUI.LogFile != "" {
		f, openErr := os.OpenFile(cfg.DebugUI.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if openErr != nil {
			return fmt.Errorf("open debug log: %w", openErr)
		}
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	runtime.GOMAXPROCS(cfg.GOMAXPROCS)
	debug.SetGCPercent(200)
	if err := platform.LowerProcessPriority(); err != nil {
		log.Printf("无法降低进程优先级：%v", err)
	}

	audioContext, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("初始化 Windows 音频系统：%w", err)
	}
	defer func() { _ = audioContext.Uninit(); audioContext.Free() }()
	devices, err := device.List(audioContext)
	if err != nil {
		return err
	}
	if *listDevices {
		for _, d := range devices {
			fmt.Printf("%d\t%s\t默认=%v\t输入通道=%d\n", d.Index, d.Name, d.Default, d.MaxChannels)
		}
		return nil
	}
	deviceIndex := cfg.Audio.DeviceIndex
	if *deviceOverride != -2 {
		deviceIndex = *deviceOverride
	}
	modelID := cfg.ASR.ModelID
	if *modelOverride != "" {
		modelID = *modelOverride
	}

	// 设备选择：优先使用命令行参数，其次使用上次记忆，最后手动选择
	var selectedDevice device.Info
	if deviceIndex >= 0 {
		// 命令行或配置指定了设备
		selectedDevice, err = chooseDevice(devices, deviceIndex)
		if err != nil {
			return err
		}
	} else if cfg.LastDeviceIndex >= 0 && *deviceOverride == -2 {
		// 尝试使用上次记忆的设备
		fmt.Printf("尝试使用上次的麦克风（编号 %d）……\n", cfg.LastDeviceIndex)
		selectedDevice, err = chooseDevice(devices, cfg.LastDeviceIndex)
		if err != nil {
			fmt.Printf("上次的麦克风不可用，请手动选择：\n")
			selectedDevice, err = chooseDevice(devices, -1)
			if err != nil {
				return err
			}
		}
	} else {
		// 手动选择
		selectedDevice, err = chooseDevice(devices, -1)
		if err != nil {
			return err
		}
	}

	if *micTest > 0 {
		return testMicrophone(audioContext, selectedDevice, *micTest)
	}
	if *asrBenchmark > 0 {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		executable, runtimeErr := ensureRuntime(ctx, cfg.RuntimeDir)
		if runtimeErr != nil {
			return runtimeErr
		}
		return benchmarkASR(ctx, audioContext, selectedDevice, *asrBenchmark, executable, cfg)
	}

	// 模型选择：优先使用命令行参数，其次使用上次记忆，最后手动选择
	var selectedModel model.Meta
	if modelID != "" {
		// 命令行或配置指定了模型
		selectedModel, err = chooseModel(modelID, cfg.ModelDir)
		if err != nil {
			return err
		}
	} else if cfg.LastModelID != "" && *modelOverride == "" {
		// 尝试使用上次记忆的模型
		fmt.Printf("尝试使用上次的模型（%q）……\n", cfg.LastModelID)
		selectedModel, err = chooseModel(cfg.LastModelID, cfg.ModelDir)
		if err != nil {
			fmt.Printf("上次的模型不可用，请手动选择：\n")
			selectedModel, err = chooseModel("", cfg.ModelDir)
			if err != nil {
				return err
			}
		}
	} else {
		// 手动选择
		selectedModel, err = chooseModel("", cfg.ModelDir)
		if err != nil {
			return err
		}
	}

	// 保存本次选择的设备和模型，供下次启动使用
	cfg.LastDeviceIndex = selectedDevice.Index
	cfg.LastModelID = selectedModel.ID
	if saveErr := cfg.Save(*configPath); saveErr != nil {
		log.Printf("无法保存设备/模型选择到配置文件：%v", saveErr)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if !model.Installed(selectedModel, cfg.ModelDir) {
		fmt.Printf("模型尚未安装，将验证官方链接并下载：%s\n", selectedModel.Name)
		if err := download(ctx, selectedModel, cfg.ModelDir); err != nil {
			return err
		}
	}
	if err := model.ValidateInstalled(selectedModel, cfg.ModelDir); err != nil {
		return err
	}
	executable, err := ensureRuntime(ctx, cfg.RuntimeDir)
	if err != nil {
		return err
	}
	if err := cfg.Validate(!*check); err != nil {
		return err
	}
	if *check {
		fmt.Printf("检查通过：设备=%s；模型=%s；ASR=%s\n", selectedDevice.Name, selectedModel.Name, executable)
		return nil
	}

	asrStarted := time.Now()
	recognizer, err := asr.NewWithThreads(executable, filepath.Join(cfg.ModelDir, selectedModel.ID), selectedModel.Kind, cfg.ASR.NumThreads)
	if err != nil {
		return err
	}
	defer recognizer.Close()
	if cfg.Debug {
		log.Printf("[DEBUG] ASR 常驻服务加载完成：耗时=%v，模型=%s，线程=%d", time.Since(asrStarted), selectedModel.ID, cfg.ASR.NumThreads)
	}
	translator := translate.NewWithConfig(translate.Config{APIKey: cfg.DeepSeek.APIKey, Endpoint: cfg.DeepSeek.Endpoint, Model: cfg.DeepSeek.Model, Timeout: cfg.TranslationTimeout(), Retries: cfg.DeepSeek.Retries})
	glossaryStore, err := glossary.LoadDir(cfg.Translation.GlossaryDir, cfg.AutoDeduplicateGlossary())
	if err != nil {
		return fmt.Errorf("load game glossaries: %w", err)
	}
	live, err := liveconfig.New(cfg.Translation.SettingsFile, liveconfig.Settings{ActiveProfile: cfg.Translation.ActiveProfile, CorrectionMode: cfg.Translation.CorrectionMode, ChineseSource: cfg.Subtitle.ChineseSource, ContextSentences: cfg.Translation.ContextSentences, CustomPrompt: cfg.Translation.CustomPrompt}, glossaryStore)
	if err != nil {
		return err
	}
	pageCfg := wsserver.PageConfig{Mode: cfg.Subtitle.Mode, HideAfterMS: cfg.Subtitle.HideAfterMS, EnglishFontSize: cfg.Subtitle.EnglishFontSize, ChineseFontSize: cfg.Subtitle.ChineseFontSize, PositionX: cfg.Subtitle.PositionX, PositionY: cfg.Subtitle.PositionY, MaxWidth: cfg.Subtitle.MaxWidth, EnglishColor: cfg.Subtitle.EnglishColor, ChineseColor: cfg.Subtitle.ChineseColor, StrokeColor: cfg.Subtitle.StrokeColor, Background: cfg.Subtitle.Background, FontFamily: cfg.Subtitle.FontFamily, ChineseSource: cfg.Subtitle.ChineseSource}
	server := wsserver.NewWithPage(cfg.Listen, webpage.SubtitleHTML, pageCfg)
	server.SetModelsPage(webpage.ModelsHTML)
	server.SetDashboardPage(webpage.DashboardHTML)
	server.SetEditorPage(webpage.EditorHTML)
	server.SetDebugPage(webpage.DebugHTML)
	server.SetPromptPage(webpage.PromptHTML)
	server.SetModelDir(cfg.ModelDir)
	server.SetCurrentModel(selectedModel.ID)
	server.SetControlCallbacks(controlCallbacks(live, server))
	server.SetChineseSource(live.Current().ChineseSource)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start() }()

	audioCfg := audio.DefaultConfig()
	audioCfg.SilenceSamples = audio.SampleRate * cfg.Audio.SilenceMS / 1000
	audioCfg.MinVoiceSamples = audio.SampleRate * cfg.Audio.MinSpeechMS / 1000
	audioCfg.MaxSegmentSamples = audio.SampleRate * cfg.Audio.MaxSpeechSecond
	audioCfg.PreRollSamples = audio.SampleRate * cfg.Audio.PreRollMS / 1000
	collector := audio.NewWithConfig(audioContext, selectedDevice.ID, audioCfg)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	defer collector.Stop()

	server.SetDebugEnabled(cfg.Debug)
	flow := &pipeline.Integrator{ASR: recognizer, Translator: translator, Output: server, Logger: log.Default(), DebugFunc: server.IsDebugEnabled, ASRModel: selectedModel.ID, MaxSegmentSecond: cfg.Audio.MaxSpeechSecond}
	flow.BuildRichRequest = func(source string, history []string) translate.RichRequest {
		snapshot := live.Snapshot(source)
		contextCount := snapshot.ContextSentences
		if contextCount > len(history) {
			contextCount = len(history)
		}
		recent := append([]string(nil), history[len(history)-contextCount:]...)
		terms := make([]translate.GlossaryTerm, 0, len(snapshot.Terms))
		for _, term := range snapshot.Terms {
			terms = append(terms, translate.GlossaryTerm{Source: term.Source, Target: term.Target, Aliases: append([]string(nil), term.Aliases...), Category: term.Category, Protected: term.Protected})
		}
		return translate.RichRequest{Source: source, RecentContext: recent, ActiveProfile: snapshot.ActiveProfile, CorrectionMode: snapshot.CorrectionMode, BackgroundPrompt: snapshot.CustomPrompt, SystemPrompt: snapshot.SystemPrompt, Glossary: terms}
	}
	flow.DebugSink = func(event pipeline.DebugEvent) {
		_ = server.BroadcastDebug(wsserver.DebugEvent{SegmentID: event.SegmentID, DurationMS: event.DurationMS, SegmentReason: event.SegmentReason, ASRModel: event.ASRModel, Raw: event.Raw, Corrected: event.Corrected, English: event.English, Diff: event.Diff, Profile: event.Profile, Terms: event.MatchedTerms, Context: event.Context, Latencies: map[string]float64{"asr": float64(event.ASRMS), "translate": float64(event.TranslateMS), "total": float64(event.TotalMS)}, Tokens: event.TotalTokens, TokenUsage: map[string]int{"prompt": event.PromptTokens, "cache_hit": event.CacheHitTokens, "cache_miss": event.CacheMissTokens, "output": event.OutputTokens, "total": event.TotalTokens}, CacheHit: event.CacheHitTokens > 0, Retries: max(event.Attempts-1, 0), Error: event.Error, RequestBody: event.RequestBody, ResponseBody: event.ResponseBody, TS: float64(event.Timestamp.UnixMilli()) / 1000})
	}
	flowDone := make(chan error, 1)
	go func() { flowDone <- flow.Run(ctx, collector.Segments()) }()
	fmt.Printf("实时字幕已启动（%s）\n麦克风：%s\n模型：%s\n", version, selectedDevice.Name, selectedModel.Name)
	for _, base := range serviceURLs(cfg.Listen) {
		fmt.Printf("OBS 字幕 URL：%s/subtitle\n字幕预览编辑器：%s/editor\n", base, base)
	}
	localBase := serviceURLs(cfg.Listen)[0]
	fmt.Printf("翻译与术语控制（仅本机）：%s/control\n实时 DEBUG 面板（仅本机）：%s/debug\n模型管理（仅本机）：%s/models\n", localBase, localBase, localBase)
	fmt.Printf("管理面板（仅本机）：%s/dashboard\n", localBase)
	if cfg.Debug {
		go debugMonitor(ctx, collector, flow, server)
	}

	var result error
	select {
	case <-ctx.Done():
	case result = <-serverErrors:
		cancel()
	case result = <-flowDone:
		cancel()
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = server.Shutdown(shutdownCtx)
	collector.Stop()
	stats, capture := flow.Stats(), collector.Stats()
	log.Printf("退出统计：语音段=%d，识别=%d，翻译=%d，错误=%d，最后延迟=%dms，音频丢帧=%d，字幕丢段=%d", stats.Captured, stats.Recognized, stats.Translated, stats.Errors, stats.LastLatencyMS, capture.CallbackDropped, capture.SegmentsDropped)
	return result
}

func serviceURLs(listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return []string{"http://127.0.0.1:8765"}
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return []string{"http://" + net.JoinHostPort(host, port)}
	}
	result := []string{"http://" + net.JoinHostPort("127.0.0.1", port)}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip := ipnet.IP.To4()
		if ip != nil && !(ip[0] == 169 && ip[1] == 254) {
			result = append(result, "http://"+net.JoinHostPort(ip.String(), port))
		}
	}
	return result
}

func debugMonitor(ctx context.Context, collector *audio.Collector, flow *pipeline.Integrator, server *wsserver.Server) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c := collector.Stats()
			p := flow.Stats()
			log.Printf("[DEBUG] 运行状态：客户端=%d，回调=%d，回调丢帧=%d，断句=%d，断句丢弃=%d，ASR=%d，翻译=%d，最后耗时(asr=%dms/translate=%dms/total=%dms)", server.ClientCount(), c.CallbackFrames, c.CallbackDropped, c.SegmentsEmitted, c.SegmentsDropped, p.Recognized, p.Translated, p.LastASRMS, p.LastTranslateMS, p.LastLatencyMS)
		}
	}
}

func testMicrophone(audioContext *malgo.AllocatedContext, selected device.Info, duration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	collector := audio.New(audioContext, selected.ID)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("正在从真实麦克风采集 %s：%s\n", duration, selected.Name)
	<-ctx.Done()
	collector.Stop()
	stats := collector.Stats()
	if stats.CallbackFrames == 0 || stats.SamplesCaptured == 0 {
		return errors.New("麦克风已打开，但没有收到任何音频帧")
	}
	fmt.Printf("麦克风测试通过：回调=%d，样本=%d，回调丢帧=%d，语音段=%d\n", stats.CallbackFrames, stats.SamplesCaptured, stats.CallbackDropped, stats.SegmentsEmitted)
	return nil
}

func benchmarkASR(parent context.Context, audioContext *malgo.AllocatedContext, selected device.Info, duration time.Duration, executable string, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	audioCfg := audio.DefaultConfig()
	audioCfg.SilenceSamples = audio.SampleRate * cfg.Audio.SilenceMS / 1000
	audioCfg.MinVoiceSamples = audio.SampleRate * cfg.Audio.MinSpeechMS / 1000
	audioCfg.MaxSegmentSamples = audio.SampleRate * cfg.Audio.MaxSpeechSecond
	audioCfg.PreRollSamples = audio.SampleRate * cfg.Audio.PreRollMS / 1000
	collector := audio.NewWithConfig(audioContext, selected.ID, audioCfg)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("正在使用真实麦克风录制 ASR 对比样本（%s）：%s\n", duration, selected.Name)
	var segments [][]byte
	for segment := range collector.Segments() {
		segments = append(segments, append([]byte(nil), segment...))
	}
	collector.Stop()
	if len(segments) == 0 {
		return errors.New("没有检测到有效语音；请靠近麦克风重新运行 ASR 对比")
	}
	var tested int
	for _, candidate := range model.Catalog {
		if !model.Installed(candidate, cfg.ModelDir) {
			continue
		}
		recognizer, err := asr.NewWithThreads(executable, filepath.Join(cfg.ModelDir, candidate.ID), candidate.Kind, cfg.ASR.NumThreads)
		if err != nil {
			fmt.Printf("\n[%s] 加载失败：%v\n", candidate.Name, err)
			continue
		}
		started := time.Now()
		var audioSeconds float64
		fmt.Printf("\n[%s]\n", candidate.Name)
		for i, segment := range segments {
			audioSeconds += float64(len(segment)) / 32000
			text, transcribeErr := recognizer.Transcribe(segment)
			if transcribeErr != nil {
				fmt.Printf("  %d. 识别失败：%v\n", i+1, transcribeErr)
				continue
			}
			fmt.Printf("  %d. %s\n", i+1, text)
		}
		elapsed := time.Since(started)
		_ = recognizer.Close()
		rtf := 0.0
		if audioSeconds > 0 {
			rtf = elapsed.Seconds() / audioSeconds
		}
		fmt.Printf("  音频 %.2fs；识别耗时 %v；实时倍率 RTF=%.3f\n", audioSeconds, elapsed, rtf)
		tested++
	}
	if tested == 0 {
		return errors.New("尚未安装任何可用于对比的 ASR 模型")
	}
	return nil
}

func chooseDevice(devices []device.Info, configured int) (device.Info, error) {
	if configured >= 0 {
		for _, d := range devices {
			if d.Index == configured {
				return d, nil
			}
		}
		return device.Info{}, fmt.Errorf("配置的麦克风编号 %d 不存在", configured)
	}
	return device.Select(devices)
}
func chooseModel(id, dir string) (model.Meta, error) {
	if id != "" {
		if m, ok := model.Find(id); ok {
			return m, nil
		}
		return model.Meta{}, fmt.Errorf("配置的模型 %q 不存在", id)
	}
	fmt.Println("\n可用 ASR 模型：")
	return model.Select(os.Stdin, os.Stdout, dir)
}

func controlCallbacks(live *liveconfig.Manager, server *wsserver.Server) wsserver.ControlCallbacks {
	toControl := func(settings liveconfig.Settings) wsserver.ControlState {
		entries := live.Terms(settings.ActiveProfile)
		terms := make([]wsserver.Term, 0, len(entries))
		for _, entry := range entries {
			terms = append(terms, wsserver.Term{Source: entry.Source, Target: entry.Target})
		}
		return wsserver.ControlState{Profile: settings.ActiveProfile, CustomPrompt: settings.CustomPrompt, CorrectionMode: settings.CorrectionMode, ChineseSource: settings.ChineseSource, ContextSize: settings.ContextSentences, Terms: terms, UpdatedAt: float64(time.Now().UnixMilli()) / 1000}
	}
	return wsserver.ControlCallbacks{
		Get: func(context.Context) (wsserver.ControlState, error) {
			return toControl(live.Current()), nil
		},
		Apply: func(_ context.Context, state wsserver.ControlState) (wsserver.ControlState, error) {
			if state.ResetTerms {
				updated, err := live.ResetProfile(state.Profile)
				if err != nil {
					return wsserver.ControlState{}, err
				}
				return toControl(updated), nil
			}
			var entries []glossary.Entry
			if state.Terms != nil {
				entries = make([]glossary.Entry, 0, len(state.Terms))
				for _, term := range state.Terms {
					entries = append(entries, glossary.Entry{Source: term.Source, Target: term.Target, Category: state.Profile, Protected: true})
				}
			}
			updated, err := live.Apply(liveconfig.Settings{ActiveProfile: state.Profile, CorrectionMode: state.CorrectionMode, ChineseSource: state.ChineseSource, ContextSentences: state.ContextSize, CustomPrompt: state.CustomPrompt}, entries)
			if err != nil {
				return wsserver.ControlState{}, err
			}
			server.SetChineseSource(updated.ChineseSource)
			return toControl(updated), nil
		},
	}
}

func download(ctx context.Context, m model.Meta, dir string) error {
	return model.DownloadWithOptions(ctx, m, dir, model.DownloadOptions{Retries: 2, Progress: func(p model.Progress) {
		if p.Total > 0 {
			fmt.Printf("\r%.1f%%  %.1f MB/s", float64(p.Downloaded)*100/float64(p.Total), p.BytesPerSecond/1024/1024)
		}
	}})
}

func ensureRuntime(ctx context.Context, dir string) (string, error) {
	exe := filepath.Join(dir, "sherpa-onnx-v1.13.4", "bin", "sherpa-onnx-offline.exe")
	serverExe := filepath.Join(dir, "sherpa-onnx-v1.13.4", "bin", "sherpa-onnx-offline-websocket-server.exe")
	if st, err := os.Stat(exe); err == nil && st.Size() > 0 {
		if serverStat, serverErr := os.Stat(serverExe); serverErr == nil && serverStat.Size() > 0 {
			return exe, nil
		}
	}
	m := model.Meta{ID: "sherpa-onnx-v1.13.4", Name: "sherpa-onnx Windows x64 v1.13.4", Kind: "runtime", Language: "n/a", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.4/sherpa-onnx-v1.13.4-win-x64-shared-MD-Release-no-tts.tar.bz2", RequiredFiles: []string{"bin/sherpa-onnx-offline.exe", "bin/sherpa-onnx-offline-websocket-server.exe"}}
	fmt.Println("正在验证并下载官方 sherpa-onnx Windows 运行库……")
	if err := download(ctx, m, dir); err != nil {
		return "", fmt.Errorf("安装 ASR 运行库：%w", err)
	}
	if st, err := os.Stat(exe); err != nil || st.Size() == 0 {
		return "", errors.New("ASR 运行库下载完成但缺少 sherpa-onnx-offline.exe")
	}
	return exe, nil
}
