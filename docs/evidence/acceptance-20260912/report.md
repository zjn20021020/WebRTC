# 固定验收报告

- 时间：2026-09-12T09:08:21.529Z
- 结果：通过
- 验收集：home-acceptance-v1；60 个样例，每例 3 次
- 模型：deepseek-v4-pro
- 提交：2d0a76fc9833c950f398e481c7619e02d3c26453；实现摘要：b324b11e1ad0351cd7498b97f7a3c9d082fcb1400d57d7f755d6d05a5bffbf4b
- TTS 音色：101016；采样率：8000

## 文本分类

| 项目 | 通过 / 计划 | 严格通过率 | 请求错误 | 未执行 | 延迟 P50 / P95（ms） |
| --- | ---: | ---: | ---: | ---: | ---: |
| 动作分类 | 54 / 54 | 100.00% | 0 | 0 | 906 / 1216 |
| 打断分类 | 126 / 126 | 100.00% | 0 | 0 | 802 / 919 |

误打断：0 / 60（0.00%）；漏打断：0 / 66（0.00%）。

分母分别为已执行的预期 false / true 样本。请求异常按运行时 false 兜底计入有效决策，但严格通过率始终将异常计为失败。未执行样本另列，不进入误/漏率分母。

## 真实语音闭环

通过 5 / 5；旧轮取消后恢复事件 0；动作兜底 0；打断兜底 0；动作重试 0。

| 场景 | 轮次 | 结果 | 证据 |
| --- | ---: | --- | --- |
| deferred-wait | 1 | 通过 | [JSON](media/deferred-wait-1/home-wait-voice.json) |
| first-praise | 1 | 通过 | [JSON](media/first-praise-1/home-praise-voice.json) |
| explicit-replacement | 1 | 通过 | [JSON](media/explicit-replacement-1/home-replacement-voice.json) |
| direct-switch | 1 | 通过 | [JSON](media/direct-switch-1/home-switch-voice.json) |
| discard-old-buffer | 1 | 通过 | [JSON](media/discard-old-buffer-1/home-clear-buffer-voice.json) |

## 延迟

| 指标 | 样本数 | P50（ms） | P95（ms） | 最大值（ms） |
| --- | ---: | ---: | ---: | ---: |
| 浏览器确认事件到取消事件 | 3 | 130 | 140 | 140 |
| 媒体链路动作分类（含内部重试） | 13 | 855 | 1077 | 1077 |
| 媒体链路打断分类请求 | 9 | 731 | 1240 | 1240 |
| direct: speech_end_to_first_text_ms | 5 | 1032 | 1376 | 1376 |
| direct: asr_final_to_first_text_ms | 5 | 839 | 1054 | 1054 |
| direct: speech_end_to_first_audio_ms | 5 | 1822 | 2111 | 2111 |
| direct: asr_final_to_first_audio_ms | 5 | 1608 | 1788 | 1788 |
| direct: speech_to_duck_ms | 5 | 9 | 18 | 18 |
| deferred: speech_end_to_first_text_ms | 5 | 18225 | 19181 | 19181 |
| deferred: asr_final_to_first_text_ms | 5 | 17924 | 19015 | 19015 |
| deferred: speech_end_to_first_audio_ms | 5 | 18839 | 20098 | 20098 |
| deferred: asr_final_to_first_audio_ms | 5 | 18538 | 19932 | 19932 |
| interrupted: speech_end_to_first_text_ms | 3 | 2014 | 2184 | 2184 |
| interrupted: asr_final_to_first_text_ms | 3 | 1892 | 1898 | 1898 |
| interrupted: speech_end_to_first_audio_ms | 3 | 2678 | 2806 | 2806 |
| interrupted: asr_final_to_first_audio_ms | 3 | 2520 | 2555 | 2555 |
| interrupted: speech_to_duck_ms | 3 | 230 | 272 | 272 |

## 未通过样例

无。

## 测量边界

- 文本层直接调用实际分类客户端，每次只请求一次，不包含 ASR、partial 调度门槛或动作内部重试。媒体层运行完整应用和实际云服务。
- P50/P95 使用 nearest-rank；请求耗时包含错误和超时。JSON 另列合法响应耗时、分类混淆矩阵、分类别和逐样例结果。
- 重复输入用于检查回归和波动，不代表独立用户样本，也不承诺线上准确率。
- 响应指标在每次媒体运行中按轮次取最后一个已知值，避免累计快照重复计数；direct 为直接响应，interrupted 为打断后响应，deferred 为缓存派发，后者包含有意等待。speech_to_duck 每轮仅保留最后一次已知值。
- 首音频指服务端首个非静音 RTP 发送；确认到取消指浏览器收到控制事件的间隔。均不是物理耳机延迟。
- 媒体输入为固定合成音频经 WebAudio 虚拟麦克风进入真实 WebRTC；物理输出静音。旧轮恢复检查不等于耳机尾音测量。
- 未覆盖真人噪声/回声、多说话人、跨 final 合并和长期弱网。
