package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	debugOverride := flag.Bool("debug", false, "启用详细调试日志（覆盖配置）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *debugOverride {
		cfg.Debug = true
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
	selectedDevice, err := chooseDevice(devices, deviceIndex)
	if err != nil {
		return err
	}
	if *micTest > 0 {
		return testMicrophone(audioContext, selectedDevice, *micTest)
	}
	selectedModel, err := chooseModel(modelID, cfg.ModelDir)
	if err != nil {
		return err
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
	pageCfg := wsserver.PageConfig{Mode: cfg.Subtitle.Mode, HideAfterMS: cfg.Subtitle.HideAfterMS, EnglishFontSize: cfg.Subtitle.EnglishFontSize, ChineseFontSize: cfg.Subtitle.ChineseFontSize, PositionX: cfg.Subtitle.PositionX, PositionY: cfg.Subtitle.PositionY, MaxWidth: cfg.Subtitle.MaxWidth, EnglishColor: cfg.Subtitle.EnglishColor, ChineseColor: cfg.Subtitle.ChineseColor, StrokeColor: cfg.Subtitle.StrokeColor, Background: cfg.Subtitle.Background, FontFamily: cfg.Subtitle.FontFamily}
	server := wsserver.NewWithPage(cfg.Listen, webpage.SubtitleHTML, pageCfg)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start() }()

	audioCfg := audio.DefaultConfig()
	audioCfg.SilenceSamples = audio.SampleRate * cfg.Audio.SilenceMS / 1000
	audioCfg.MinVoiceSamples = audio.SampleRate * cfg.Audio.MinSpeechMS / 1000
	audioCfg.MaxSegmentSamples = audio.SampleRate * cfg.Audio.MaxSpeechSecond
	collector := audio.NewWithConfig(audioContext, selectedDevice.ID, audioCfg)
	if err := collector.Start(ctx); err != nil {
		return err
	}
	defer collector.Stop()

	flow := &pipeline.Integrator{ASR: recognizer, Translator: translator, Output: server, Logger: log.Default(), Debug: cfg.Debug}
	flowDone := make(chan error, 1)
	go func() { flowDone <- flow.Run(ctx, collector.Segments()) }()
	fmt.Printf("实时字幕已启动（%s）\n麦克风：%s\n模型：%s\n", version, selectedDevice.Name, selectedModel.Name)
	for _, base := range serviceURLs(cfg.Listen) {
		fmt.Printf("OBS 字幕 URL：%s/subtitle\n字幕预览编辑器：%s/editor\n", base, base)
	}
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
