# 当前版本 0.5

# 0.3 版本更新目的

1. [feature] 接入更多语音识别模型，语音识别模型目前只有 4 个，是否可以接入列表式交互，让用户有更多的模型可选，并且新增模型管理页面，可交互式下载，应用，删除模型，建议是新增 HTML 页面来管理这些，更友好的操作。注意，这里是要接入更多的语音识别模型，是需要给用户提供一个可选择下载的列表，而这个列表应该基于现在互联网上一些最新的语音识别模型的列表，而不只是一个单纯的管理页面。
2. [bug] glossaries 文件夹内 JSON 可能会存在重复，会导致程序启动失败且直接关闭窗口，用户根本没法判断错误原因，所以需要做此类容错处理，当有重复 JSON 时，在 config 里新增参数，如果为 True 则默认为用户排重，该参数默认为 True。
3. [opt] 在 DEBUG 时应开放 Deepseek API 报文内容，这样更便于判断 PROMPT 的问题，当然你也可以更细化这块的 DEBUG 内容。
4. [opt] 当前管理页面已经有3或4个，我希望整理统一的入口来管理，而不只是在终端内给出对应管理功能的单独链接，这样对于用户来说非常麻烦。
5. [opt] 优化程序启动后流程，比如增加默认选择上一次的麦克风设备和模型设备，应该记录到本地 config，减少用户每次输入的启动。

完成时间：2026-08-17

# 0.4 版本更新目的

1. [opt] 优化向 API 发送的 Prompt 内容，根据检查，现在发送的 Prompt 只要选择了本地的glossaries内容，会发送非常庞大的内容，占用了大量的 TOKEN 资源，这块要着重优化！减少 TOKEN 消耗，但不能影响实际翻译效果，也不要影响性能。

完成时间：2026-08-17

# 0.5 版本更新目的

1. [bug] 实际运行软件时会有一些错误日志
```
(Client.Timeout or context cancellation while reading body)
2026/08/18 13:17:10 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:17:25 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:17:41 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:18:09 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:18:24 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:18:40 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:18:55 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:19:10 context deadline exceeded (Client.Timeout or context cancellation while reading body)
2026/08/18 13:19:26 context deadline exceeded (Client.Timeout or context cancellation while reading body)
```
2. [OPT] 优化向 AI API 发送的 TOKEN 数量，看是让 AI 形成一个长期思考的惯性还是怎么样，这里可以给我一个方案，我们再决定开发。
3. [OPT] DEBUG 模式在 /debug 页面应该增加动态开启或关闭的功能，方便用户快速启用调试和关闭调试
4. [OPT] 默认给 AI API 发送的 PROMPT 应可以修改，创建一个新的 WEB 路由页面来管理这些内容。
5. [OPT] /models 路由页面并未显示正在使用的模型。
6. [OPT] /models 路由页面需要增加更多的模型下载功能，希望通过浏览器能力在网上寻找到一个 API 接口来获取更多最新的 ASR 模型，我们不是要扩展到对应数量，而是扩展成动态拉取的能力

完成时间：2026-08-18