# WebRTC 双向音频实验

使用 Go + Pion 建立浏览器与服务端之间的 WebRTC 语音会话，接入腾讯云 ASR、DeepSeek V4 Pro 和腾讯云 TTS。

当前版本已经包含：

- `POST /api/offer` 非 Trickle ICE 信令；
- 浏览器麦克风上行音频；
- 服务端长期存在的 PCMU 下行音轨；
- 服务端接收上行 RTP 并输出帧日志；
- 服务端 PCMU 解码和 RMS 音量日志；
- 服务端基于 RMS 的 VAD 事件和 DataChannel 通知；
- 服务端将 TTS PCM16 编码为 PCMU，通过持续存在的音轨播放；
- 腾讯云实时 ASR：8kHz PCM 输入、临时字幕和最终文本；
- DeepSeek V4 Pro 流式回答、分句合成和会话内上下文；
- 回答轮次管理、有限音频队列、新语音确认后取消旧回答、手动停止；
- 前端连接状态、回答文本、合成/播放状态、远端音频和基础日志。

ASR 和 TTS 使用腾讯云，LLM 使用 DeepSeek 的 `deepseek-v4-pro`。LLM 采用非思考模式，只播放回答正文。

## 运行

需要 Go 1.22 或更高版本：

```bash
go mod tidy
go run ./cmd/server
```

打开 <http://localhost:8080>，点击“连接并启用麦克风”，允许浏览器访问麦克风。

连接后下行音轨持续发送静音保活。对着麦克风提问，页面先显示识别字幕，再逐步显示回答；语音按句合成后播放。服务端每收到 50 个上行 RTP 包输出 `pcmu_rms`，连续语音约 200ms 后输出 `vad event=speech_started`。测试双音生成工具仍保留在音频模块中，但连接时不再自动播放测试音。

## 腾讯云 ASR 配置

1. 在 [语音识别控制台](https://console.cloud.tencent.com/asr) 开通服务，确保可使用实时识别引擎 `8k_zh`。
2. 从 [账号信息](https://console.cloud.tencent.com/developer) 获取 AppID，从 [API 密钥管理](https://console.cloud.tencent.com/cam/capi) 获取同账号下的 SecretId 和 SecretKey。使用子账号时需授予调用语音识别服务的权限。
3. 在项目根目录 `.env` 中填入以下内容，字段模板见 `.env.example`：

```dotenv
TENCENT_APP_ID=
TENCENT_SECRET_ID=
TENCENT_SECRET_KEY=
```

服务启动时读取 `.env`，系统环境变量优先。凭证变更后需重启服务并重新连接浏览器；凭证仅在 Go 服务端使用，`.env` 已被 Git 忽略。

未配置凭证时，音频和 VAD 仍正常工作，字幕区显示“未配置凭证”。配置后字幕状态依次变为“连接识别服务中”“识别中”；说话过程中更新临时字幕，服务端断句后保留最终文本。点击“断开”会停止采集并关闭识别连接。

```text
WebRTC PCMU / 8kHz / mono
  -> PCM16 signed little-endian (无 WAV 文件头)
  -> 3200 字节 / 200ms 二进制 WebSocket 消息
  -> 腾讯云 8k_zh
  -> slice_type 0/1: asr_partial; slice_type 2: asr_final
  -> DataChannel -> 页面字幕
```

ASR 持续接收全部音频，包括静音和下行播放期间的上行声音，服务端 RMS VAD 不截断识别音频。ASR 使用独立、有限长度的队列；超过约 2 秒的积压时停止本次识别并报告网络拥堵，麦克风接收和下行播放继续工作。识别失败后可断开并重新连接；当前不自动续接云端任务。

协议依据：[腾讯云实时语音识别 WebSocket API](https://cloud.tencent.com/document/product/1093/48982)。请求由服务端执行 HMAC-SHA1 签名，保留语气词，采用 600ms 静音断句；句子 ID 由云端会话 ID 和句子序号组成，与未来的回答轮次独立。

## DeepSeek 与腾讯 TTS 配置

在同一份 `.env` 中增加以下字段。`DEEPSEEK_URL` 可使用 API 根地址、`/v1` 地址或完整 `/chat/completions` 地址；缺省地址为官方接口。密钥只在服务端使用。

```dotenv
DEEPSEEK_API_KEY=
DEEPSEEK_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-v4-pro
TENCENT_TTS_VOICE_TYPE=1001
```

腾讯 TTS 复用 `TENCENT_SECRET_ID` 和 `TENCENT_SECRET_KEY`，需开通语音合成并具有调用权限。默认使用音色 `1001`；可在腾讯音色列表中选择其他支持 8kHz 的音色。设置后重启服务。

```text
浏览器麦克风 -> WebRTC PCMU/8kHz -> PCM16LE -> 腾讯 ASR
  -> asr_final -> DeepSeek V4 Pro SSE -> 回答文本 / 分句
  -> 腾讯 TextToVoice (PCM16LE, 8kHz, mono)
  -> PCMU -> 160 字节 / 20ms RTP -> 浏览器音频输出
```

LLM 返回流式正文；TTS 当前使用官方 Go SDK 的 `TextToVoice`，每句合成完后进入播放队列，并非 TTS WebSocket 逐音频块输出。分句上限为 100 个字符，队列最多保留约 5 秒的音频；合成等待不会阻塞麦克风接收。服务端不需要重采样。

每次最终识别文本启动一个 `response_epoch`。新句子的非空 ASR 临时文本、另一条最终文本或页面“停止回答”会取消旧 LLM/TTS 请求，并丢弃旧轮次待播音频。重复的最终识别事件不会重复回答，晚到的云端结果不能重新进入当前播放队列。断开会释放本会话的识别、生成和播放资源。

最近约 12 条对话消息用于会话内上下文，只有完整发送到下行的回答会写入助手历史。当前服务仍只维护一个浏览器会话，新连接会关闭上一会话。

当前打断以 ASR 文本确认为准，能量 VAD 仅上报状态；尚未实现“VAD 触发软打断、ASR 确认硬打断”的两阶段策略。已经发往浏览器抖动缓冲区的音频可能保留少量尾音，建议使用耳机。浏览器已启用回声消除请求。

协议依据：[DeepSeek 模型说明](https://api-docs.deepseek.com/quick_start/pricing)、[非思考模式参数](https://api-docs.deepseek.com/guides/thinking_mode)、[腾讯 TextToVoice](https://cloud.tencent.com/document/api/1073/37995)。

## 验证

```bash
go test ./...
go vet ./...
```

本地测试覆盖 ASR 签名和流式协议、DeepSeek SSE 及取消、TTS 请求参数和 PCM16 字节序、分句长度、轮次替换、旧音频清理、队列限长及错误传播。默认测试不调用付费云接口。装有 C 编译器并启用 CGO 的环境可另外执行 `go test -race ./internal/dialogue ./internal/llm ./internal/tts ./internal/rtc`。

已使用真实腾讯 ASR、DeepSeek V4 Pro、腾讯 TTS 和浏览器虚拟麦克风完成短句联调：识别字幕、生成回答、下行非零音频能量、重连和手动停止均通过。桌面及 390px 宽度页面也已检查。实际麦克风、扬声器回声及网络变化下的体验仍需在目标设备测试。

## 下一步

实现两阶段打断与误触发恢复，并记录首字、首音频和打断延迟；随后优化 TTS 流式输出和真实回声环境下的体验。
