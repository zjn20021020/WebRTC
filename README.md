# WebRTC 双向音频实验

这是全双工语音打断实验的第一阶段：使用 Go + Pion 建立浏览器与服务端之间的 WebRTC 音频会话。

当前版本已经包含：

- `POST /api/offer` 非 Trickle ICE 信令；
- 浏览器麦克风上行音频；
- 服务端长期存在的 PCMU 下行音轨；
- 服务端接收上行 RTP 并输出帧日志；
- 服务端 PCMU 解码和 RMS 音量日志；
- 服务端基于 RMS 的 VAD 事件和 DataChannel 通知；
- 前端连接状态、远端音频和基础日志。

## 运行

需要 Go 1.22 或更高版本：

```bash
go mod tidy
go run ./cmd/server
```

打开 <http://localhost:8080>，点击“连接并启用麦克风”，允许浏览器访问麦克风。

服务端每收到 50 个上行 RTP 包会输出 `pcmu_rms`，连续语音约 200ms 后会输出 `vad event=speech_started`，并通过 DataChannel 通知浏览器。对着麦克风说话时，波形和 RMS 应明显高于安静状态。

## 下一步

下一步是在 VAD 事件边界上接入流式 ASR、`ResponseEpoch` 轮次管理、两阶段打断、TTS 和下行音频队列。
