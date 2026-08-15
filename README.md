# 实时语音翻译字幕系统

Windows 游戏主机上的低资源后台服务：真实麦克风 → 实时 VAD → sherpa-onnx 本地 ASR → DeepSeek 中译英 → WebSocket → OBS。

## 快速开始

1. 打开项目根目录的 `config.json`，填写 `deepseek.api_key`。
2. 双击或在 PowerShell 运行 `subtitle-translator.exe`。
3. 启动时选择真实麦克风和 ASR 模型。未安装的模型及 sherpa-onnx 运行库会先验证官方链接，再自动下载。
4. 程序启动后会打印“OBS 字幕 URL”和“字幕预览编辑器”URL。B 电脑的 OBS 添加“浏览器”源，直接粘贴形如 `http://192.168.10.189:8765/subtitle` 的地址，无需复制本地 HTML。
5. 在普通浏览器打开形如 `http://A电脑IP:8765/editor` 的编辑器，可实时模拟和调整模式、保留时间、字号、位置、宽度、颜色、背景与字体，然后复制生成的 OBS URL。

6. Windows 防火墙允许 TCP 8765。按 Ctrl+C 可安全退出。

## 配置

实际配置是 `config.json`；`config.example.json` 是可恢复的模板。真实密钥文件已加入 `.gitignore`。环境变量 `DEEPSEEK_API_KEY` 只在 JSON 未填写时作为后备。

- `audio.device_index`: `-1` 表示启动时选择；也可填写固定编号。
- `asr.model_id`: 空值表示启动时选择；可选 `paraformer-zh`、`sensevoice-int8`、`whisper-tiny`、`whisper-base`、`whisper-small`。
- `asr.num_threads`: 默认 2，最大允许 4。
- `listen`: 默认 `:8765`。
- `debug`: 设为 `true` 后输出断句大小/时长、中文识别结果、英文翻译结果、各阶段耗时、队列错误以及每 10 秒的采集与客户端统计。
- `subtitle.mode`: `bilingual` 为中英双语（中文小、英文大），`english` 为纯英文。
- `subtitle.hide_after_ms`: 字幕保留时间；设为 `0` 表示一直保留到下一条字幕。
- `subtitle.english_font_size` / `chinese_font_size`: 分别配置英文字号和中文字号。
- `subtitle.position_x_percent` / `position_y_percent`: 画布中的水平和垂直位置。
- `subtitle.max_width_percent`、颜色、背景和字体均可在 JSON 或网页编辑器中配置。
- `model_dir` / `runtime_dir`: 相对于配置文件所在目录解析。

## 检查与诊断

```powershell
# 枚举 Windows 当前真实输入设备
.\subtitle-translator.exe --list-devices

# 真正打开编号 0 的麦克风采集 3 秒，不使用离线音频代替
.\subtitle-translator.exe --device=0 --mic-test=3s

# 验证模型、官方运行库和配置
.\subtitle-translator.exe --device=0 --model=paraformer-zh --check
```

命令行的 `--device`、`--model` 只覆盖本次运行，不修改 JSON。

## 构建与测试

需要 Go 1.21+ 和 Windows x64 C 编译器：

```powershell
go test -race ./...
go build -trimpath -ldflags "-s -w" -o subtitle-translator.exe ./cmd/translator
```

## 实现说明

- 音频：WASAPI/miniaudio 真实采集端点，16kHz、单声道、S16、30ms 块。
- 回调：预分配固定缓冲，仅复制、非阻塞入队及原子计数，不做 I/O 或推理。
- VAD：后台 Google WebRTC VAD（模式 2），初始化失败时回退到自适应能量与过零检测；150ms 预录、500ms 静音、最短 300ms、最长 10 秒。
- ASR：官方 sherpa-onnx v1.13.4 Windows x64 常驻服务，模型只加载一次；模型参数自动适配，推理互斥且默认 2 线程。
- 下载：HEAD/Range 双重探测、断点续传、重试、速度显示、可选 SHA-256、模型文件清单检查以及原子安全解压。
- 流水线：有界队列、单 ASR worker、单翻译 worker、panic 隔离及丢弃统计，避免积压影响游戏。
- HTTP/OBS：服务端直接托管透明字幕页和实时编辑器；默认保留 12 秒、2 秒自动重连，支持双语/纯英文和 URL 临时覆盖配置。

测试与实机结果见 `TEST_REPORT.md`。
