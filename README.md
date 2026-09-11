# WebRTC 双向音频实验

这是全双工语音打断实验的第一阶段：使用 Go + Pion 建立浏览器与服务端之间的 WebRTC 音频会话。

当前版本已经包含：

- `POST /api/offer` 非 Trickle ICE 信令；
- 浏览器麦克风上行音频；
- 服务端长期存在的 PCMU 下行音轨；
- 服务端接收上行 RTP 并输出帧日志；
- 服务端 PCMU 解码和 RMS 音量日志；
- 服务端基于 RMS 的 VAD 事件和 DataChannel 通知；
- 服务端下行固定双音测试音，并将 PCM16 编码为 PCMU；
- 腾讯云实时 ASR：8kHz PCM 输入、临时字幕和最终文本；
- 前端连接状态、远端音频和基础日志。

项目后续统一优先使用腾讯产品：ASR 使用腾讯云实时语音识别，LLM 计划使用腾讯混元，TTS 计划使用腾讯云语音合成。目前尚未接入 LLM 和 TTS。

## 运行

需要 Go 1.22 或更高版本：

```bash
go mod tidy
go run ./cmd/server
```

打开 <http://localhost:8080>，点击“连接并启用麦克风”，允许浏览器访问麦克风。

连接后服务端会先通过下行音轨播放约 2 秒固定双音测试音，再发送 PCMU 静音帧保活。服务端每收到 50 个上行 RTP 包会输出 `pcmu_rms`，连续语音约 200ms 后会输出 `vad event=speech_started`，并通过 DataChannel 通知浏览器。对着麦克风说话时，波形和 RMS 应明显高于安静状态。

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

## 验证

```bash
go test ./...
go vet ./...
```

本地测试使用模拟 WebSocket 服务验证签名、音频字节序、发送节奏、临时/最终结果、取消和错误处理。真实识别效果、云端账号权限和计费需配置有效凭证后联调；固定测试音不用于验证识别准确率。

## 下一步

完成腾讯云真实语音联调后，接入 `ResponseEpoch` 轮次管理、腾讯混元/规则回答、腾讯云 TTS、两阶段打断和下行音频队列。
