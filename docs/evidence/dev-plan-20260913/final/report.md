# 固定验收报告

- 时间：2026-09-13T15:42:28.997Z
- 结果：通过
- 验收集：home-acceptance-v2-plan；73 个样例，每例 1 次
- 模型：deepseek-v4-pro
- 提交：bde731e83edfb38625128d19d99cce5e0bc6dd7e；实现摘要：39ae902b446bb1a4fbc68a42df0aba62baa83e8727a2c6fff921dbe906b0c74f
- TTS 音色：101016；采样率：8000

## 文本分类

| 项目 | 通过 / 计划 | 严格通过率 | 请求错误 | 未执行 | 延迟 P50 / P95（ms） |
| --- | ---: | ---: | ---: | ---: | ---: |
| 动作分类 | 29 / 29 | 100.00% | 0 | 0 | 1040 / 1241 |
| 打断分类 | 44 / 44 | 100.00% | 0 | 0 | 737 / 951 |

误打断：0 / 21（0.00%）；漏打断：0 / 23（0.00%）。

分母分别为已执行的预期 false / true 样本。请求异常按运行时 false 兜底计入有效决策，但严格通过率始终将异常计为失败。未执行样本另列，不进入误/漏率分母。

## 真实语音闭环

通过 8 / 8；旧轮取消后恢复事件 0；动作兜底 0；打断兜底 0；动作重试 0。

| 场景 | 轮次 | 结果 | 证据 |
| --- | ---: | --- | --- |
| deferred-wait | 1 | 通过 | [JSON](media/deferred-wait-1/home-wait-voice.json) |
| first-praise | 1 | 通过 | [JSON](media/first-praise-1/home-praise-voice.json) |
| explicit-replacement | 1 | 通过 | [JSON](media/explicit-replacement-1/home-replacement-voice.json) |
| direct-switch | 1 | 通过 | [JSON](media/direct-switch-1/home-switch-voice.json) |
| discard-old-buffer | 1 | 通过 | [JSON](media/discard-old-buffer-1/home-clear-buffer-voice.json) |
| ordered-plan | 1 | 通过 | [JSON](media/ordered-plan-1/plan-sequence.json) |
| cancel-plan | 1 | 通过 | [JSON](media/cancel-plan-1/plan-cancel.json) |
| cross-final-wait | 1 | 通过 | [JSON](media/cross-final-wait-1/plan-merge.json) |

## 延迟

| 指标 | 样本数 | P50（ms） | P95（ms） | 最大值（ms） |
| --- | ---: | ---: | ---: | ---: |
| 浏览器确认事件到取消事件 | 4 | 0 | 8 | 8 |
| 媒体链路动作分类（含内部重试） | 19 | 1127 | 1273 | 1273 |
| 媒体链路打断分类请求 | 13 | 609 | 906 | 906 |
| direct: speech_end_to_first_text_ms | 8 | 2981 | 3176 | 3176 |
| direct: asr_final_to_first_text_ms | 8 | 1945 | 2083 | 2083 |
| direct: speech_end_to_first_audio_ms | 8 | 3645 | 3954 | 3954 |
| direct: asr_final_to_first_audio_ms | 8 | 2614 | 2743 | 2743 |
| direct: speech_to_duck_ms | 8 | 0 | 240 | 240 |
| deferred: speech_end_to_first_text_ms | 7 | 18651 | 58085 | 58085 |
| deferred: asr_final_to_first_text_ms | 7 | 17561 | 57061 | 57061 |
| deferred: speech_end_to_first_audio_ms | 7 | 19242 | 58867 | 58867 |
| deferred: asr_final_to_first_audio_ms | 7 | 18152 | 57843 | 57843 |
| interrupted: speech_end_to_first_text_ms | 4 | 3374 | 3431 | 3431 |
| interrupted: asr_final_to_first_text_ms | 4 | 2308 | 2448 | 2448 |
| interrupted: speech_end_to_first_audio_ms | 4 | 3998 | 4072 | 4072 |
| interrupted: asr_final_to_first_audio_ms | 4 | 2932 | 3069 | 3069 |
| interrupted: speech_to_duck_ms | 3 | 0 | 0 | 0 |
| planned: speech_end_to_first_text_ms | 2 | 21863 | 41483 | 41483 |
| planned: asr_final_to_first_text_ms | 2 | 20814 | 40434 | 40434 |
| planned: speech_end_to_first_audio_ms | 2 | 22543 | 42083 | 42083 |
| planned: asr_final_to_first_audio_ms | 2 | 21494 | 41035 | 41035 |

## 未通过样例

无。

## 测量边界

- 文本层直接调用实际分类客户端，每次只请求一次，不包含 ASR、partial 调度门槛或动作内部重试。媒体层运行完整应用和实际云服务。
- P50/P95 使用 nearest-rank；请求耗时包含错误和超时。JSON 另列合法响应耗时、分类混淆矩阵、分类别和逐样例结果。
- 重复输入用于检查回归和波动，不代表独立用户样本，也不承诺线上准确率。
- 响应指标在每次媒体运行中按轮次取最后一个已知值，避免累计快照重复计数；direct 为直接响应，interrupted 为打断后响应，deferred 为缓存派发，planned 为计划后续步骤，后两者包含有意等待。speech_to_duck 每轮仅保留最后一次已知值。
- 首音频指服务端首个非静音 RTP 发送；确认到取消指浏览器收到控制事件的间隔。均不是物理耳机延迟。
- 媒体输入为固定合成音频经 WebAudio 虚拟麦克风进入真实 WebRTC；物理输出静音。旧轮恢复检查不等于耳机尾音测量。
- 跨 final 合并仅在 cross-final-wait 场景通过且记录至少两个来源 final 时计为已测；文本分类测试不代表跨 final 调度已验证。未覆盖真人噪声/回声、多说话人和长期弱网。
