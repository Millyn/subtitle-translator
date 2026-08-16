# 测试与性能报告

测试日期：2026-08-15；平台：Windows x64；CPU：Intel Core i5-13490F；Go 1.25.13。

## 自动化验证

- `go test -race ./...`：全部通过，未发现数据竞争。
- 内部包总语句覆盖率：75.4%。
- 主要包：ASR 81.1%、翻译 82.5%、运行时配置 83.8%、流水线 94.8%、术语 76.5%、WebSocket/HTTP 75.6%、模型 72.5%、设备 71.7%、音频 59.6%。
- 音频未覆盖部分主要是必须依赖真实 Windows 驱动的打开、停止和错误分支，没有使用离线音频冒充硬件验收。

自动化测试覆盖：

- Whisper 从 Catalog 和运行分支移除。
- SenseVoice 自动语言、FireRedASR2 CTC、FunASR Nano 参数和必需文件校验。
- 下载 URL 探测、Range 续传、长度拒绝、SHA-256、原子解压和路径穿越防护。
- DeepSeek 结构化 JSON、保守纠错、关闭纠错、Prompt 注入隔离、非 JSON、缺少英文、重试和 Token Usage。
- 纠正中文缺失时回退原始中文，且不采信 AI 返回的伪造原始文本。
- 最近上下文、丰富字幕广播、翻译失败降级、DEBUG 事件和统计。
- iRacing、Minecraft、Project Zomboid 术语加载、匹配、替换、删除、持久化和恢复内置版本。
- 控制 API 校验、本机访问限制、Profile 切换不串用术语、普通 OBS 页面保持局域网可访问。
- 多客户端 WebSocket、并发写、断线清理和安全关闭。

## 模型下载链接验证

新增模型的官方链接已经实际联网请求，不是只检查字符串：

| 模型 | 最终结果 | 压缩包字节数 | Range |
|---|---|---:|---|
| FireRedASR2 CTC zh_en INT8 | GitHub 302 → `release-assets.githubusercontent.com` 200 | 520,516,278 | 支持 |
| FunASR Nano INT8 | GitHub 302 → `release-assets.githubusercontent.com` 200 | 841,730,611 | 支持 |

现有 Paraformer、SenseVoice 链接在上一版本已逐一验证，仍使用 sherpa-onnx 官方 `asr-models` Release。当前本地 sherpa-onnx v1.13.4 的离线 CLI 和 WebSocket Server 帮助信息均已确认支持四个正式模型，不需要更换运行库。

## 真实麦克风与服务验收

最终 v1.2.0 使用本机真实 `Microphone (NVIDIA Broadcast)` 启动：

- Paraformer 常驻服务加载约 1.32 秒。
- 连续测试期间收到 4,334 次以上真实音频回调，丢帧 0。
- 一轮完整 DEBUG 验收捕获 10 个真实断句，识别 10 个，字幕段丢弃 0。
- 实际语音段约 1.05～5.69 秒；Paraformer 单段识别约 20～78ms。
- 真实识别结果、断句原因、模型、耗时和模拟 API 连接错误均实时进入 `/debug`。
- 麦克风测试配置故意指向不可用的本地 DeepSeek 地址，验证 AI 失败时错误可观察，且流水线仍广播原始中文降级字幕。
- Ctrl+C 正常关闭并输出最终统计。

另使用项目现有配置向真实 DeepSeek 发送一条无私人内容的合成验收句。服务端成功返回 `corrected_chinese/english/was_corrected/matched_terms` 四个结构化字段，并将“这个三是不错的”结合上下文修复为“这个确实是不错的”，同时生成英文。密钥没有出现在命令输出、日志、控制 API 或网页中；常规自动化测试仍全部使用本地模拟服务。

## 浏览器与访问控制验收

- `/control` 实测切换 `auto → iracing` 无需重启，立即加载 55 条 iRacing 术语。
- 上下文 `2 → 3`、中文来源 `corrected → compare` 和自定义 Prompt 均即时保存；浏览器刷新后仍保持。
- `/debug` WebSocket 实际接收麦克风产生的 ASR 事件，显示原文、模型、时长、断句原因、耗时和错误。
- DEBUG 表格在 1280×720 浏览器中完成视觉检查，长日志可滚动且列宽稳定。
- `127.0.0.1/control` 返回 200；通过本机局域网地址访问 `/control` 返回 403；同一局域网地址访问 `/subtitle` 返回 200。

## 轻量性能基准（上一版基线）

| 路径 | 结果 | 内存分配 |
|---|---:|---:|
| 30ms Adaptive VAD | 780ns/op | 0 B/op，0 allocs/op |
| 麦克风采集回调 | 114ns/op | 0 B/op，0 allocs/op |
| 无客户端 WebSocket 广播 | 314ns/op | 96 B/op，2 allocs/op |

采集回调继续满足不阻塞和零分配约束。网络翻译耗时取决于 DeepSeek、网络和 Prompt 大小，不包含在微基准中。

## 长时间直播验收边界

CPU、内存、端到端延迟和连续运行一小时仍需在实际游戏负载、目标麦克风、真实 DeepSeek 网络以及最终选择的模型下测量。新版提供 `--asr-benchmark=15s`，可以用同一批真实麦克风语音比较所有已安装模型的文本、耗时和 RTF。
