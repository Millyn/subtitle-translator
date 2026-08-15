# 测试与性能报告

测试日期：2026-08-15；平台：Windows x64；CPU：Intel Core i5-13490F；Go 1.25.13。

## 自动化验证

- `go test -race ./...`：全部通过，未发现数据竞争。
- `go test -coverprofile=coverage.out ./internal/...`：加入 WebRTC VAD 后，内部包语句总覆盖率 **74.0%**。
- 核心包覆盖率：config 100.0%、pipeline 96.8%、ws 86.1%、asr 79.9%、translate 75.4%、model 72.1%、device 71.7%、audio 59.6%。
- audio 未覆盖部分主要是必须依赖真实 Windows 音频设备的启动、停止和设备驱动错误分支；platform 是 Windows 进程优先级系统调用。它们不适合由伪造的离线音频替代。
- 下载测试覆盖 URL 的 HEAD/GET 探测、Range 续传、进度回调、长度不完整拒绝、SHA-256 校验、模型文件清单、原子解压与路径穿越防护。
- 流水线测试覆盖正常广播、空识别、ASR/翻译错误、空翻译、关闭输入、配置缺失和 worker panic 隔离。
- WebSocket 测试覆盖多客户端、并发广播、断开清理、无客户端广播与上下文取消关闭。
- HTTP 字幕页、编辑器、配置接口及中英双语消息已经加入自动化测试；浏览器实测编辑器能够切换纯英文、设置持续显示、调整字号和位置，并生成对应 OBS URL。

## 轻量性能基准

以下结果是本机短时微基准，并非一小时直播负载测试：

| 路径 | 结果 | 内存分配 |
|---|---:|---:|
| 30 ms Adaptive VAD | 780 ns/op | 0 B/op，0 allocs/op |
| 麦克风采集回调 | 114 ns/op | 0 B/op，0 allocs/op |
| 单段流水线 | 5.72 µs/op | 873 B/op，16 allocs/op |
| 无客户端 WebSocket 广播 | 314 ns/op | 96 B/op，2 allocs/op |

采集回调满足“不阻塞、零分配”的设计约束。基准只衡量程序自身调度，不包含 ASR 推理与 DeepSeek 网络时间。

## 已完成的真实环境验证

- 已枚举本机真实 Windows 麦克风输入端点，不使用离线音频冒充设备验证。
- NVIDIA Broadcast 麦克风短时采集约 3 秒：99 次回调、47,520 个样本、回调丢帧 0。
- NVIDIA Broadcast 麦克风改用 WebRTC VAD 后再次采集约 3 秒：98 次回调、47,040 个样本、回调丢帧 0。
- Paraformer INT8 模型和 sherpa-onnx v1.13.4 常驻服务已完成真实中文样本集成验证；5.611 秒样本的常驻推理约 130ms。测试在模型或运行库不存在时会明确跳过。
- 完整服务（真实麦克风、常驻 ASR、字幕 WebSocket）短时运行时，Go 主进程约 13.9MB、ASR 进程约 148.2MB，合计约 162.1MB；10 秒空闲观察期累计 CPU 时间约 0.2 秒、无丢帧并能由 Ctrl+C 正常关闭。
- DeepSeek 自动化测试使用本地模拟 HTTP 服务，不读取或暴露用户真实密钥。

## 最终硬件验收边界

CPU ≤10%、内存 ≤300 MB、端到端延迟 ≤3 秒及连续运行一小时必须在实际直播游戏负载、目标麦克风、网络和所选模型下测量。当前自动化测试、竞态检查、短时真机采集及微基准均通过，但不能代替这一小时现场验收。推荐起始配置为 Paraformer INT8、ASR 2 线程、`gomaxprocs=3`。
