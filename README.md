# 实时语音翻译字幕系统

Windows 游戏主机上的实时字幕服务：真实麦克风 → VAD 断句 → sherpa-onnx 本地中英 ASR → DeepSeek 保守纠错与翻译 → HTTP/WebSocket → OBS。

## 快速开始

1. 在 `config.json` 的 `deepseek.api_key` 中填写密钥。
2. 运行 `subtitle-translator.exe`，选择真实麦克风和 ASR 模型。
3. 未安装的模型会先验证官方链接，再断点下载并校验文件。
4. 将程序打印的局域网 `OBS 字幕 URL` 粘贴到电脑 B 的 OBS“浏览器来源”。
5. 本机打开程序打印的 `/control`，可以在不重启程序和 OBS 的情况下切换游戏、Prompt、纠错、上下文和术语。

程序提供以下页面：

- `/subtitle`：OBS 透明字幕。
- `/editor`：字号、位置、颜色、保留时间等视觉设置和模拟预览。
- `/control`：游戏 Profile、AI 纠错、自定义 Prompt 和术语表，仅电脑 A 本机可访问。
- `/debug`：原始 ASR、纠正中文、英文、上下文、术语、耗时、Token、缓存和错误，可动态开关，仅本机可访问。
- `/prompt`：System Prompt 和背景提示管理，仅电脑 A 本机可访问。
- `/models`：ASR 模型管理，显示当前使用模型，支持从 GitHub 动态获取远程模型列表。

## 中国场景 ASR 模型

项目不再提供 Whisper。可选模型：

- `paraformer-zh`：速度最快、资源占用低，通用中文和少量英文。
- `sensevoice-int8`：默认均衡选择，使用自动语言检测，适合中文夹英文。
- `fire-red-asr2-ctc-int8`：中文/英文和多种中文方言，高准确率档，约 497 MiB。
- `funasr-nano-int8`：中文/英文/日文及多种口音，实验性高精度档，约 803 MiB，资源占用最高。

移除模型目录中的 Whisper 入口不会删除用户以前下载的文件。

## 纠错、翻译和游戏术语

DeepSeek 每个断句只请求一次，同时返回：

- 原始 ASR 中文（由程序保存，不接受 AI 改写）
- 保守纠正后的中文
- 英文翻译
- 是否发生纠正
- 命中的术语
- API 尝试次数和 Token 使用

默认携带最近两条纠正后的中文作为上下文。自定义 Prompt 是直播背景和翻译风格；程序固定规则会阻止语音转录内容覆盖系统指令，并要求无法确认时保留原文。

内置术语位于 `glossaries/`：

- iRacing：55 条起始术语
- Minecraft：52 条起始术语
- Project Zomboid：60 条起始术语

控制页支持即时切换 `auto/general/iracing/minecraft/project_zomboid/disabled`，以及术语添加、删除、JSON/CSV 导入、JSON 导出和恢复内置版本。修改保存在 `translation-settings.json`，不重写包含 API Key 的 `config.json`。

## 配置要点

- `audio.device_index`：`-1` 启动时选择真实麦克风。
- `audio.silence_ms`：默认 700ms，较完整地保留中英混合句子。
- `audio.pre_roll_ms`：默认 250ms，降低句首被切掉的概率。
- `asr.model_id`：空值启动时选择，也可填写上面的模型 ID。
- `asr.num_threads`：默认 2，最大 4。
- `deepseek.timeout_ms`：API 请求超时，默认 60000ms（60 秒），建议保持默认值。
- `translation.active_profile`：默认 `auto`。
- `translation.correction_mode`：`conservative` 或 `off`。
- `translation.context_sentences`：`0～5`，默认 2。
- `translation.custom_prompt`：用户直播背景和翻译风格，也可通过 `/prompt` 页面编辑。
- `subtitle.chinese_source`：`corrected`、`raw` 或 `compare`。
- `debug`：启用终端详细日志和网页实时调试事件，运行时可通过 `/debug` 页面动态开关。
- `debug_options.log_file`：非空时额外写入本地日志文件，密钥不会记录。
- `listen`：默认 `:8765`。

字幕字号、位置、最大宽度、颜色、背景、字体和保留时间继续通过 `subtitle` 配置或 `/editor` 调整。

## 检查与诊断

```powershell
# 枚举真实 Windows 输入设备
.\subtitle-translator.exe --list-devices

# 真正打开编号 0 的麦克风采集 3 秒
.\subtitle-translator.exe --device=0 --mic-test=3s

# 使用真实麦克风录制 15 秒，同一批语音依次交给全部已安装模型
.\subtitle-translator.exe --device=0 --asr-benchmark=15s

# 验证设备、指定模型、运行库和配置
.\subtitle-translator.exe --device=0 --model=sensevoice-int8 --check

# 本次运行启用完整 DEBUG
.\subtitle-translator.exe --debug
```

`--device`、`--model` 和 `--debug` 只覆盖本次运行，不修改 JSON。

## 实现说明

- 麦克风：WASAPI/miniaudio，16kHz、单声道、S16、30ms 块。
- 实时回调：预分配缓冲、非阻塞入队、零动态分配。
- VAD：Google WebRTC VAD，失败时回退自适应能量检测。
- ASR：官方 sherpa-onnx v1.13.4 Windows x64 常驻服务，模型只加载一次。
- 下载：HEAD/Range 探测、断点续传、重试、长度检查、可选 SHA-256 和安全原子解压。
- 流水线：有界队列、单 ASR worker、单翻译 worker、panic 隔离、错误降级和丢弃统计。
- 安全：OBS 页面允许局域网访问；控制页、调试页和调试 WebSocket 默认仅回环地址可访问。

## 构建与测试

需要 Go 1.21+ 和 Windows x64 C 编译器：

```powershell
go test -race ./...
go build -trimpath -ldflags "-s -w" -o subtitle-translator.exe ./cmd/translator
```

验证记录见 `TEST_REPORT.md`。
