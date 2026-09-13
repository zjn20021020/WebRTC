# 自动音频路由与 AEC

测试分支：`codex/aec-headphone-priority`，基于主分支 `0910c10`。本分支撤下 40% 基础音量和 ASR RMS 能量门限，恢复原下行音量及完整 PCM 上行；疑似插话仍保留原有的 50% duck。跨 final 合并、复合任务规划、意图判断和完整取消闭环沿用主分支。

## 分流规则

浏览器的 `MediaDeviceInfo.kind` 只能区分音频输入、音频输出、视频输入，没有标准的“耳机/扬声器”硬件类型字段。因此自动识别属于尽力判断，不承诺所有设备都能正确识别。

1. 初次申请麦克风一律关闭 AEC、降噪和自动增益，先建立可用的原音轨道。
2. 获取麦克风权限后，通过 `enumerateDevices()` 查询实际输出设备名称；只看 `audiooutput`，不会根据“耳机麦克风”推断声音从耳机输出。
3. Windows 本机运行时，前端从 `GET /api/audio-output` 获取系统默认输出端点的 FriendlyName 和 FormFactor。此接口只接受 loopback 请求，并禁止缓存；远程浏览器不能把服务器的硬件误当成自己的硬件。
4. 输出名称包含耳机、耳麦、Headphones、Headset、Earphones、Earbuds 或 AirPods 时，优先按耳机处理。没有耳机名称时，再结合 Windows 耳机/扬声器属性或明确的扬声器名称。
5. 输出信息不可用、名称无法判断、非默认输出不可见或设备信息冲突时，优先保留原音；浏览器明确选择其他输出时，不套用服务器的系统默认设备。

本机实测 Windows 返回 `kind=speakers`、`name=扬声器 (HECATE G2 GAMING HEADSET)`。该设备按名称规则识别为耳机，避免因 USB 耳机上报 Speakers 属性而启用 AEC。

| 判断结果 | 麦克风处理 |
| --- | --- |
| 耳机 | AEC 关闭，降噪和自动增益关闭，原始麦克风轨道直接上行 |
| 扬声器 | 浏览器原生 AEC 开启，降噪和自动增益关闭 |
| 不确定 | 保留原音并记录 `output_type_unavailable` 等原因；外放保护可能不可用 |
| AEC 请求失败或未生效 | 保留原可用轨道，记录 `constraints_failed` 或 `aec_not_enabled`，不伪报开启成功 |

## AEC 链路

```text
耳机：麦克风原音 ─────────────────────────→ WebRTC 上行 → ASR
扬声器：麦克风 → 浏览器原生 AEC ──────────→ WebRTC 上行 → ASR
                     ↑
          浏览器播放中的远端音频参考
```

原生 WebRTC 音频处理器负责播放参考、声学路径估计、回声消除及双方同时讲话的处理。应用保持远端 `audio` 元素播放与原 PeerConnection 链路，不增加独立 WASM 播放图或文本回声判定。

浏览器回归发现，对已创建的音轨调用 `applyConstraints` 可能正常返回，但 `getSettings().echoCancellation` 仍保持旧值。当前通过新的 `getUserMedia` 请求获取所需处理模式，并检查实际设置；新轨道准备好后再 `RTCRtpSender.replaceTrack`，同时切换波形输入，最后停止旧轨道。相同模式下不重新申请采集，耳机原音路径不做额外处理。

监听 `devicechange`，并在连接期间每 2 秒检查输出变化。采集切换过程中仍保留旧轨道；断开时停止轮询、清理音轨，迟到返回的新轨道立即停止，避免重新激活旧连接。切换 AEC 会重建采集处理器，短暂过渡及 AEC 重新收敛仍可能存在，不能宣称切换时完全无损。

## 运行

源码 ZIP 解压后，在含 `go.mod` 的 `code` 目录配置自己的 `.env`，运行：

```powershell
go run ./cmd/server
```

打开 `http://localhost:8080`。无需安装 Node、额外音频库、WASM 或模型文件。`ASR_GATE_RMS` 已不使用，旧 `.env` 保留它也不会影响识别。需要诊断日志时运行 `go run ./cmd/server -addr :8082 -diagnostics`。

页面运行日志记录 `音频路由`、实际 `AEC=true/false`、`reason` 和 `source`。诊断模式的 `browser_audio` 增加 `output_kind`、`output_reason`，继续记录采集 RMS、音轨状态、上下行和页面健康指标，不上传原始音频或设备 ID。

## 验证与边界

| 检查 | 结果与口径 |
| --- | --- |
| Windows 本机端点读取 | 取得 HECATE 耳机的系统属性与名称，上述复合规则可识别为耳机 |
| `node --test scripts/audio_route.test.cjs` | 通过：路由判定、USB 耳机上报 Speakers、默认/显式输出、输入名称隔离、未知设备、获取新模式、失败保留原轨、断开时清迟到轨道 |
| `node scripts/verify_audio_routing.cjs` | 通过：使用合成麦克风、模拟输出名称和浏览器内 WebRTC 对端；模拟 TTS 持续播放及 1600 个状态事件期间，耳机接收 RMS 最低约 `0.0861`，原音轨道直接发送；扬声器初次连接和动态开启 AEC、切回耳机关闭 AEC、未知设备原音、重连、AEC 采集请求失败时保留原轨 |
| `node scripts/verify_capture.cjs` | 通过：原始采集图在 1800 个事件、GC 和挂起恢复下保持非零，重连后原音约束仍正确 |
| `go test ./...` | 通过：现有组件回归和本机设备接口访问边界检查 |
| `go vet ./...` | 通过：静态检查 |
| Windows / Linux amd64 构建 | 均通过；非 Windows 平台使用浏览器设备信息回退 |

测试程序和记录输出：[路由单元测试](../scripts/audio_route.test.cjs)、[浏览器回归](../scripts/verify_audio_routing.cjs)，后者输出到 `bin/audio-routing/results.json`。这些自动测试没有使用付费云接口，也没有模拟真实房间声学环境；不能代替实际外放验收。

自动识别仍有明确边界：有线耳机插在某些模拟插孔上时，名称与属性都可能始终是 Speakers；HDMI、通用 USB 名称、浏览器单独输出路由或操作系统的每应用路由覆盖也可能无法准确识别。未知设备优先原音意味着它如果实际是外放，仍可能发生回声自我识别。识别为扬声器并成功开启浏览器 AEC，也不保证所有驱动、房间和音量条件下都能消除回声。

物理耳机测试需确认：播放中说“去种地”“给我讲个故事”，波形和首字完整；物理外放测试需分别确认：保持安静时不识别自身 TTS，以及播报中说“去施肥”时能完整识别并切换动作。二者均需在实际设备上验证，本分支没有把自动测试结果写成这两个现场测试已经通过。

接口参考：[MediaDeviceInfo](https://developer.mozilla.org/en-US/docs/Web/API/MediaDeviceInfo)、[echoCancellation](https://developer.mozilla.org/en-US/docs/Web/API/MediaTrackConstraints/echoCancellation)、[Windows FormFactor](https://learn.microsoft.com/en-us/windows/win32/coreaudio/pkey-audioendpoint-formfactor)。
