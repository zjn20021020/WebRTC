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
- 回答轮次管理、有限音频队列、两阶段打断、误触发恢复和手动停止；
- 前端连接状态、回答文本、合成/播放/暂停状态、延迟观测、远端音频和基础日志。

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

每次有效最终识别文本启动一个 `response_epoch`。新句子的有效 ASR 临时文本、另一条最终文本或页面“停止回答”会取消旧 LLM/TTS 请求，并丢弃旧轮次待播音频。空白和纯标点不视作有效文本，中文单字、字母、数字均可确认插话。重复的最终识别事件不会重复回答，晚到的云端结果不能重新进入当前播放队列。停止命令携带目标轮次，避免延迟到达的旧命令停止新回答。断开会释放本会话的识别、生成和播放资源。

最近约 12 条对话消息用于会话内上下文，只有完整发送到下行的回答会写入助手历史。当前服务仍只维护一个浏览器会话，新连接会关闭上一会话。

## 两阶段打断

1. 连续约 200ms 音量超过 VAD 阈值后，立即软暂停当前回答：保留轮次、播放位置、待播队列和 LLM/TTS 任务，下行改发 PCMU 静音。暂停期间 ASR 继续接收音频，队列仍受原有长度限制。
2. ASR 返回另一句的有效文本后，硬取消旧回答。新一句的最终文本到达后生成新回答。即使 VAD 没检测到较轻的声音，有效 ASR 文本也能直接触发取消。
3. VAD 检测到约 500ms 静音后，再等待 800ms 供 ASR 确认；没有有效文本就从原位置恢复。连续噪声最多暂停 4 秒，重复的开始事件不会无限延长暂停。

恢复检查使用现有 20ms RTP 时钟，旧轮次没有独立恢复定时器。手动停止、断开或新语音确认后，旧音频不会在超时后复活。ASR 失败或结束时立即解除软暂停，并禁用后续软暂停，直到识别服务重新可用。暂停期间页面“停止回答”仍可用。

能量 VAD 无法可靠区分咳嗽、回声和语音；如果 ASR 将噪声误识别为有效文字，仍可能触发硬打断。本次联调也观察到单频测试音被转写的情况，宽带噪声的无文本恢复路径已验证。已经发往浏览器抖动缓冲区的音频可能保留少量尾音，建议使用耳机。浏览器已启用回声消除请求。

## 延迟观测

页面展示本轮的首字/首音频发送延迟，以及最近一次软暂停的延迟。服务端记录 `response_metrics`，通过 DataChannel 推送同名事件；所有指标均绑定 `response_epoch`。

| 字段 | 测量边界 |
| --- | --- |
| `speech_end_to_first_text_ms` | ASR 标记的句末音频到达服务端，到 LLM 返回首个非空正文 |
| `speech_end_to_first_audio_ms` | 同一句末，到首个非静音音频帧成功写入 RTP 下行 |
| `asr_final_to_first_text_ms` | 最终识别文本交给轮次管理器，到首个正文 |
| `asr_final_to_first_audio_ms` | 最终识别文本交给轮次管理器，到首个非静音 RTP 帧发送 |
| `speech_to_pause_ms` | 根据 VAD 回推的插话起点，到暂停后的首个静音帧成功发送 |

句末估算将腾讯 ASR 的音频偏移映射到本机接收 PCM 样本的时间；保留最近 6000 个输入包，默认约 120 秒。偏移缺失、超出范围或过旧时不填造数值，页面显示 `--`。指标不包括麦克风采集、上行网络和浏览器/耳机缓冲，不能直接视为用户实际听到的端到端延迟。

协议依据：[DeepSeek 模型说明](https://api-docs.deepseek.com/quick_start/pricing)、[非思考模式参数](https://api-docs.deepseek.com/guides/thinking_mode)、[腾讯 TextToVoice](https://cloud.tencent.com/document/api/1073/37995)。

## 验证

```bash
go test ./...
go vet ./...
```

本地测试覆盖 ASR 签名和流式协议、DeepSeek SSE 及取消、TTS 请求参数和 PCM16 字节序、分句长度、轮次替换、旧音频清理、队列限长及错误传播。两阶段打断使用可控时钟测试恢复位置、静音写入失败、持续/重复噪声、暂停中云端继续返回结果、确认后不恢复、暂停中停止/断开、ASR 失败恢复和延迟边界。默认测试不调用付费云接口。装有 C 编译器并启用 CGO 的环境可另外执行 `go test -race ./internal/dialogue ./internal/llm ./internal/tts ./internal/rtc`。

已使用真实腾讯 ASR、DeepSeek V4 Pro、腾讯 TTS 和浏览器虚拟麦克风完成短句联调：识别字幕、生成回答、下行非零音频能量、重连和手动停止均通过。桌面及 390px 宽度页面也已检查。实际麦克风、扬声器回声及网络变化下的体验仍需在目标设备测试。

2026-09-12 两阶段联调通过：320ms 宽带噪声触发软暂停后在同轮次恢复；随后“等一下，不讲故事了，请告诉我一加一等于几”取消旧任务并得到新回答。此次软暂停测得约 213ms，最后一句的首字约 430ms、首个非静音 RTP 帧约 1513ms；这些是单次服务端测量，不是稳定延迟承诺。

## 下一步

优化 TTS 流式输出，补充真实耳机/扬声器回声、咳嗽等场景的识别误触发评估，并测量浏览器端实际播放延迟。
