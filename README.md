# WebRTC 双向音频实验

使用 Go + Pion 建立浏览器与服务端之间的 WebRTC 语音会话，接入腾讯云 ASR、DeepSeek V4 Pro 和腾讯云 TTS。

当前版本已经包含：

- `POST /api/offer` 非 Trickle ICE 信令；
- 浏览器麦克风上行音频；
- 服务端长期存在的 PCMU 下行音轨；
- 服务端接收上行 RTP 并输出帧日志；
- 服务端 PCMU 解码和 RMS 音量日志；
- 服务端基于 RMS 的 VAD 事件和 DataChannel 通知；
- 腾讯 WebSocket 流式 TTS，PCM16 片段随到随转 PCMU，通过持续存在的音轨播放；
- 腾讯云实时 ASR：8kHz PCM 输入、临时字幕和最终文本；
- DeepSeek V4 Pro 流式回答、分句合成和会话内上下文；
- 回答轮次管理、有限音频队列、两阶段打断、误触发恢复和手动停止；
- 前端连接状态、回答文本、合成/播放/降音状态、当前轮次、延迟观测、远端音频和基础日志。

ASR 和 TTS 使用腾讯云，LLM 使用 DeepSeek 的 `deepseek-v4-pro`。LLM 采用非思考模式，只播放回答正文。

作业要求、实现思路、架构与状态机、已验证的结果统一维护在 [作业任务说明](docs/作业任务说明.md)；未完成的说明项保留空白。真实云服务联调记录见 [带时间戳的浏览器事件](docs/evidence/barge-in.json) 和 [服务端日志摘录](docs/evidence/server-barge-in.txt)。

## 运行

需要 Go 1.22 或更高版本：

```bash
go mod tidy
go run ./cmd/server
```

打开 <http://localhost:8080>，点击“连接并启用麦克风”，允许浏览器访问麦克风。

连接后下行音轨持续发送静音保活。对着麦克风提问，页面先显示识别字幕，再逐步显示回答；正文按句提交 TTS，音频片段到达即可进入播放队列。服务端每收到 50 个上行 RTP 包输出 `pcmu_rms`，连续语音约 200ms 后输出 `vad event=speech_started`。测试双音生成工具仍保留在音频模块中，但连接时不再自动播放测试音。

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

腾讯流式 TTS 复用 `TENCENT_APP_ID`、`TENCENT_SECRET_ID` 和 `TENCENT_SECRET_KEY`，需开通语音合成并具有调用权限。默认使用音色 `1001`；可在腾讯音色列表中选择其他支持 8kHz 的音色。设置后重启服务。

```text
浏览器麦克风 -> WebRTC PCMU/8kHz -> PCM16LE -> 腾讯 ASR
  -> asr_final -> DeepSeek V4 Pro SSE -> 回答文本 / 分句
  -> 腾讯 TextToStreamAudioWS (PCM16LE 二进制流, 8kHz, mono)
  -> PCMU -> 160 字节 / 20ms RTP -> 浏览器音频输出
```

LLM 返回流式正文；TTS 使用 `wss://tts.cloud.tencent.com/stream_ws`，收到二进制 PCM 片段即转换并入队，无需等整句合成完成。跨分片保留未完成的 PCM 样本和 RTP 帧，仅句末不足一帧时补静音。分句上限为 100 个字符，队列最多保留约 5 秒的音频；队列满时对合成施加背压，不阻塞麦克风接收。取消上下文会关闭合成 WebSocket。服务端不需要重采样。

空闲时由有效 final 启动 `response_epoch`；播放中确认插话时立即递增轮次并等待新句 final，final 到达后在已分配的轮次内生成回答。确认会取消旧 LLM/TTS 上下文并清空待播和重试帧。每次入队在锁内校验轮次和取消状态，晚到片段不能重新进入当前队列。重复 final 不会重复回答，被取消的句子也不能复活。停止命令携带目标轮次，避免延迟到达的旧命令停止新回答。断开会释放本会话的识别、生成和播放资源。

最近约 12 条对话消息用于会话内上下文，只有完整发送到下行的回答会写入助手历史。当前服务仍只维护一个浏览器会话，新连接会关闭上一会话。

## 两阶段打断

1. 连续约 200ms 音量超过 RMS 700，或另一句有效 ASR 文本到达后，进入 `ducking`：服务端把下行样本增益降为 0.2，约 -14dB。队列继续播放，LLM/TTS 和麦克风上行继续工作。
2. 去掉空白和标点，过滤纯“嗯/啊/哦/呃”等语气词。明确停止短语（“等一下”“停止”等，含单字“停”），或至少两个字符的 partial 共同前缀在多次更新中稳定 200ms，或至少两个字符的有效 final，可确认打断。旧音频已经播放时至少经历 120ms duck 再硬取消；尚未播放时可直接取消生成。
3. 确认后停止旧轮下行，取消旧 LLM/TTS、清空待播，并立即分配新轮次进入 `listening`；新句 final 到达后才开始新回答。浏览器使用同一条音轨。
4. 未确认时，VAD 检测到约 500ms 静音后再等待 800ms，随后恢复正常音量。连续噪声最多 duck 4 秒，重复的开始事件不会无限延长窗口。

恢复检查使用现有 20ms RTP 时钟，旧轮次没有独立恢复定时器。手动停止、断开或新语音确认后，旧音频不会在超时后复活。ASR 失败或结束时立即解除未确认的 duck，并禁用后续 VAD 降音，直到识别服务重新可用。降音和等待新句 final 期间页面“停止回答”仍可用。打断处理不调用前端 `pause()`。

能量 VAD 无法可靠区分咳嗽、回声和语音；如果 ASR 将噪声误识别为有效文字，仍可能触发硬打断。本次联调也观察到单频测试音被转写的情况，宽带噪声的无文本恢复路径已验证。已经发往浏览器抖动缓冲区的音频可能保留少量尾音，建议使用耳机。浏览器已启用回声消除请求。

## 延迟观测

页面展示本轮的首字/首音频发送延迟，以及本轮最近一次降音的延迟。服务端记录 `response_metrics`，通过 DataChannel 推送同名事件；所有指标均绑定 `response_epoch`。

| 字段 | 测量边界 |
| --- | --- |
| `speech_end_to_first_text_ms` | ASR 标记的句末音频到达服务端，到 LLM 返回首个非空正文 |
| `speech_end_to_first_audio_ms` | 同一句末，到首个非静音音频帧成功写入 RTP 下行 |
| `asr_final_to_first_text_ms` | 最终识别文本交给轮次管理器，到首个正文 |
| `asr_final_to_first_audio_ms` | 最终识别文本交给轮次管理器，到首个非静音 RTP 帧发送 |
| `speech_to_duck_ms` | 根据 VAD 回推的插话起点，到首个含非零音频的降音帧成功发送；ASR 单独触发时从文本到达开始 |

句末估算将腾讯 ASR 的音频偏移映射到本机接收 PCM 样本的时间；保留最近 6000 个输入包，默认约 120 秒。偏移缺失、超出范围或过旧时不填造数值，页面显示 `--`。指标不包括麦克风采集、上行网络和浏览器/耳机缓冲，不能直接视为用户实际听到的端到端延迟。

协议依据：[DeepSeek 模型说明](https://api-docs.deepseek.com/quick_start/pricing)、[非思考模式参数](https://api-docs.deepseek.com/guides/thinking_mode)、[腾讯流式语音合成 WebSocket](https://cloud.tencent.com/document/product/1073/94308)。

## 验证

```bash
go test ./...
go vet ./...
```

本地测试覆盖 ASR 签名和流式协议、DeepSeek SSE 及取消、TTS 在 final 前输出音频、PCM16 分片边界、合成 WebSocket 关闭、分句长度、轮次替换、旧音频清理、队列限长及错误传播。可控时钟验证 20% 增益、队列持续消费、稳定 partial、final 前新轮次分配、同时活动的 LLM/TTS 取消、无文本恢复和降音写入失败重试。默认测试不调用付费云接口。装有 C 编译器并启用 CGO 的环境可另外执行 `go test -race ./internal/dialogue ./internal/llm ./internal/tts ./internal/rtc`。

已使用真实腾讯 ASR、DeepSeek V4 Pro、腾讯 TTS 和浏览器虚拟麦克风完成短句联调：识别字幕、生成回答、下行非零音频能量、重连和手动停止均通过。桌面及 390px 宽度页面也已检查。实际麦克风、扬声器回声及网络变化下的体验仍需在目标设备测试。

2026-09-12 流式 TTS 与两阶段 duck 联调通过：320ms 宽带噪声触发降音后同轮恢复，降音期间音频能量和上行包数仍增长；明确插话清掉旧轮 250 帧，新轮先于对应 final 约 988ms 建立。ASR 把插话分成两个 final，最终第 3 轮回答“1+1等于2”。本次噪声降音延迟 210ms，最后一句从估算句末到首字 1017ms、到首个非静音 RTP 帧 1869ms；仅为单次服务端测量，不是延迟承诺。

复现浏览器联调需要 Node.js、Chrome、有效云凭证及已运行的服务，会实际调用云接口。原 REST `TextToVoice` 仅用于生成固定输入素材，不是运行中的 TTS 回退路径。

```powershell
E:\go\bin\go.exe run ./cmd/demo-fixtures
npm install --prefix bin/browser-check --no-save playwright
$env:NODE_PATH = (Resolve-Path 'bin/browser-check/node_modules').Path
# Chrome 不在默认位置时设置 CHROME_PATH；服务不是 8080 时设置 DEMO_URL。
node scripts/verify_barge_in.cjs
```

脚本使用虚拟麦克风输入，播放期间持续发送 RTP；成功后更新 `docs/evidence/barge-in.json`，截图保存在 `bin/`。它检查浏览器收到的音频能量，自动化运行静音，不代表已测得物理耳机声音。

## 下一步

补充真实耳机/扬声器回声、咳嗽和短暂停顿场景，评估断句拆分及误触发，并测量浏览器端实际播放延迟。
