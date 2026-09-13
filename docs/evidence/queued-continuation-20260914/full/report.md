# 固定验收报告

- 时间：2026-09-13T16:43:46.514Z
- 结果：通过
- 验收集：home-acceptance-v4-continuation；98 个样例，每例 1 次
- 模型：deepseek-v4-pro
- 提交：8fa1cca3c656977d03068debe496c749f0ae88b7；实现摘要：3ce4396c86b9ddc2b180102c354ed9b365f7e702706f841dc67454a6a1f1aa7f
- TTS 音色：101016；采样率：8000

## 文本分类

| 项目 | 通过 / 计划 | 严格通过率 | 请求错误 | 未执行 | 延迟 P50 / P95（ms） |
| --- | ---: | ---: | ---: | ---: | ---: |
| 动作分类 | 32 / 32 | 100.00% | 0 | 0 | 890 / 1175 |
| 打断分类 | 52 / 52 | 100.00% | 0 | 0 | 712 / 898 |
| 补充关系分类 | 14 / 14 | 100.00% | 0 | 0 | 644 / 1196 |

误打断：0 / 27（0.00%）；漏打断：0 / 25（0.00%）。

分母分别为已执行的预期 false / true 样本。请求异常按运行时 false 兜底计入有效决策，但严格通过率始终将异常计为失败。未执行样本另列，不进入误/漏率分母。

## 真实语音闭环

通过 4 / 4；旧轮取消后恢复事件 0；动作兜底 0；打断兜底 0；动作重试 0。

| 场景 | 轮次 | 结果 | 证据 |
| --- | ---: | --- | --- |
| discard-old-buffer | 1 | 通过 | [JSON](media/discard-old-buffer-1/home-clear-buffer-voice.json) |
| cross-final-wait | 1 | 通过 | [JSON](media/cross-final-wait-1/plan-merge.json) |
| story-amendment | 1 | 通过 | [JSON](media/story-amendment-1/story-amendment.json) |
| story-independent | 1 | 通过 | [JSON](media/story-independent-1/story-independent.json) |

## 延迟

| 指标 | 样本数 | P50（ms） | P95（ms） | 最大值（ms） |
| --- | ---: | ---: | ---: | ---: |
| 浏览器确认事件到取消事件 | 1 | 0 | 0 | 0 |
| 媒体链路动作分类（含内部重试） | 10 | 790 | 1082 | 1082 |
| 媒体链路打断分类请求 | 8 | 656 | 923 | 923 |
| 缓存补充关系分类请求 | 2 | 425 | 473 | 473 |
| direct: speech_end_to_first_text_ms | 4 | 2503 | 3162 | 3162 |
| direct: asr_final_to_first_text_ms | 4 | 1457 | 1879 | 1879 |
| direct: speech_end_to_first_audio_ms | 4 | 3193 | 3891 | 3891 |
| direct: asr_final_to_first_audio_ms | 4 | 2108 | 2608 | 2608 |
| direct: speech_to_duck_ms | 4 | 0 | 40 | 40 |
| interrupted: speech_end_to_first_text_ms | 1 | 3353 | 3353 | 3353 |
| interrupted: asr_final_to_first_text_ms | 1 | 2093 | 2093 | 2093 |
| interrupted: speech_end_to_first_audio_ms | 1 | 3960 | 3960 | 3960 |
| interrupted: asr_final_to_first_audio_ms | 1 | 2701 | 2701 | 2701 |
| interrupted: speech_to_duck_ms | 1 | 0 | 0 | 0 |
| deferred: speech_end_to_first_text_ms | 5 | 19154 | 39035 | 39035 |
| deferred: asr_final_to_first_text_ms | 5 | 17960 | 38011 | 38011 |
| deferred: speech_end_to_first_audio_ms | 5 | 19980 | 39657 | 39657 |
| deferred: asr_final_to_first_audio_ms | 5 | 18786 | 38633 | 38633 |

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

- 缓存补充合并 1 次；关系分类兜底 0 次。关系分类只合并尚未执行的相邻输入，不计入打断误/漏率。
