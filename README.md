# 迪莫家园语音助手 · WebRTC 全双工实验

使用 Go + Pion 建立浏览器与服务端之间的 WebRTC 语音会话，接入腾讯云 ASR、DeepSeek V4 Pro 和腾讯云 TTS。

当前场景固定为《洛克王国：世界》的家园精灵迪莫，默认已经唤起。支持六类原生 function calling：浇水、种菜、收菜、施肥、亲密动作、通用问答。五类动作均以固定台词重复十次模拟，通用问答带迪莫角色回复；尚未连接真实游戏。

可直接说“去种菜”，播报时说“去施肥”即可切换农务，不需要先说“别种菜了”。先降音至 50%，合法打断判断 true 才取消旧任务；“等种完再施肥”、普通问题和夸赞判 false，在当前任务音频发送完成后处理。要立即转入问答，可以说“先别种菜了，回答我一加一等于几”。

角色规则见 [迪莫 SKILL.md](internal/home/skills/dimo/SKILL.md)，游戏资料、分类协议及未来工具接口见 [场景调研与设计](docs/场景调研与设计.md)。角色 skill 内嵌进程序，修改后需重新编译并重启。

前端采用家园风格：官方原野场景与迪莫贴纸、绿白工作台、桌面双栏字幕/回答、手机纵向布局。连接、收音波形、播放器、所有状态、延迟指标和日志完整保留。预览见 [桌面截图](docs/evidence/home-ui-desktop.png) / [手机截图](docs/evidence/home-ui-mobile.png)，素材来源与图标许可见 [SOURCES.md](web/assets/SOURCES.md)。页面素材均本地加载，静态文件修改后刷新即可生效。

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
- DeepSeek 语义插话判断、严格布尔 JSON 校验、非打断输入缓存及播完自动回答；
- 独立动作分类指令、本地白名单与空参数校验、格式异常限次重试、可取消动作语音执行器和失败语音提示；
- 前端连接状态、回答文本、合成/播放/降音状态、当前轮次、延迟观测、远端音频和基础日志。

ASR 和 TTS 使用腾讯云，LLM 使用 DeepSeek 的 `deepseek-v4-pro`。LLM 采用非思考模式，只播放回答正文。

作业要求、实现思路、架构与状态机、已验证的结果统一维护在 [作业任务说明](docs/作业任务说明.md)；未完成的说明项保留空白。真实云服务联调记录见 [带时间戳的浏览器事件](docs/evidence/barge-in.json) 和 [服务端日志摘录](docs/evidence/server-barge-in.txt)。

## 运行

界面回归可运行 `node scripts/verify_ui.cjs`（Playwright 环境配置见后文）：覆盖 1440/1024/768/390/320px、21 个原有控件、图片加载、波形像素、长文本/错误状态和减少动画偏好。`verify_home.cjs` 可通过 `EVIDENCE_DIR` 指定证据输出目录，避免覆盖历史联调记录。

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
TENCENT_TTS_VOICE_TYPE=101016
```

腾讯流式 TTS 复用 `TENCENT_APP_ID`、`TENCENT_SECRET_ID` 和 `TENCENT_SECRET_KEY`，需开通语音合成并具有调用权限。默认使用精品女童声音色 `101016`（智甜），按用户偏好选择更清亮的童声方向，作为迪莫角色的近似配音，并非官方迪莫原声。该音色支持 8kHz 实时合成；语速和音量维持供应商默认值。其他可选音色见[腾讯音色列表](https://cloud.tencent.com/document/product/1073/92668)，设置后重启服务。

可用以下命令通过实际运行的流式接口生成试听，WAV 包含 PCM16 → PCMU → PCM16 转换后的 8kHz 单声道声音；命令会实际调用腾讯 TTS，输出首包耗时、音频长度和 RMS，不输出凭证：

```powershell
go run ./cmd/tts-preview -out bin/tts-preview.wav
# Compare Tencent's Zhixiaohu child voice without changing the server config:
go run ./cmd/tts-preview -voice 502007 -out bin/voice-zhixiaohu.wav
```

```text
浏览器麦克风 -> WebRTC PCMU/8kHz -> PCM16LE -> 腾讯 ASR
  -> asr_final -> DeepSeek 六类工具分类
  -> 固定动作台词 / 迪莫角色 DeepSeek SSE -> 正文分句
  -> 腾讯 TextToStreamAudioWS (PCM16LE 二进制流, 8kHz, mono)
  -> PCMU -> 160 字节 / 20ms RTP -> 浏览器音频输出
```

LLM 返回流式正文；TTS 使用 `wss://tts.cloud.tencent.com/stream_ws`，收到二进制 PCM 片段即转换并入队，无需等整句合成完成。跨分片保留未完成的 PCM 样本和 RTP 帧，仅句末不足一帧时补静音。分句上限为 100 个字符，队列最多保留约 5 秒的音频；队列满时对合成施加背压，不阻塞麦克风接收。取消上下文会关闭合成 WebSocket。服务端不需要重采样。

空闲时由有效 final 启动 `response_epoch`；播放中确认插话时立即递增轮次并等待新句 final，final 到达后在已分配的轮次内生成回答。确认会取消旧 LLM/TTS 上下文并清空待播和重试帧。每次入队在锁内校验轮次和取消状态，晚到片段不能重新进入当前队列。重复 final 不会重复回答，被取消的句子也不能复活。停止命令携带目标轮次，避免延迟到达的旧命令停止新回答。断开会释放本会话的识别、生成和播放资源。

最近约 12 条对话消息用于会话内上下文，只有完整发送到下行的回答会写入助手历史。当前服务仍只维护一个浏览器会话，新连接会关闭上一会话。

## 家园场景验证

```powershell
E:\go\bin\go.exe test ./...
E:\go\bin\go.exe vet ./...
E:\go\bin\go.exe run ./cmd/verify-home
E:\go\bin\go.exe run ./cmd/demo-fixtures -scene home
node scripts/verify_home.cjs
```

浏览器脚本需要 Node 可加载 Playwright、已安装 Chrome，并保持服务运行。可用 `DEMO_URL` 指定测试服务器；它会建立新会话并实际调用云接口。真实样例结果见 [home-classifier.json](docs/evidence/home-classifier.json) 和 [home-voice.json](docs/evidence/home-voice.json)。已验证浇水取消、施肥十次、夸赞延后贴贴十次，以及双向媒体收发。ASR 可能拆分称呼与命令，产生额外简短回应；本版本未合并跨 final 输入。

动作分类使用专用 prompt，迪莫角色 skill 只用于说话问答。官方 DeepSeek 地址的动作分类单独使用 `/beta/chat/completions`，六个函数均启用 `strict:true` 和空对象 schema，在生成端约束参数；问答和打断判定仍用原地址，自定义网关保持原路径及本地校验。协议不合法时在原 5 秒总期限内重试一次，通过校验后才执行；拒绝和网络错误不重试。仍失败时提示“任务未能启动”，日志用 `validation` 区分额外正文、多调用、截断、非法参数等，不把技术失败当作听不懂用户。

换任务专项回归可运行 `go run ./cmd/verify-home -suite replacement`、`go run ./cmd/demo-fixtures -scene home-replacement`、`node scripts/verify_home.cjs --replacement`。首次夸赞专项使用 `go run ./cmd/verify-home -suite praise`、`go run ./cmd/demo-fixtures -scene home-praise`、`node scripts/verify_home.cjs --praise`，覆盖“干的不错/干得不错”、历史上下文和种地播放结束后才执行贴贴。

## 两阶段打断

1. 连续约 200ms 音量超过 RMS 700，或另一句有效 ASR 文本到达后，进入 `ducking`：服务端把下行样本增益降为 0.5，约 -6dB。队列继续播放，LLM/TTS 和麦克风上行继续工作。
2. 同句 partial 至少两个字符的共同前缀稳定 200ms，或出现停止短语时，提前调用独立的意图判断 prompt；final 到达后用完整文本判定。“等一下/等会/稍后”等等待类 partial 则等整句 final 后再判断，避免“等一下再去种地”尚未说完就取消浇水。该完整句表示缓存种菜任务，“等一下”单独说完才表示暂停。关键词不能直接确认，只有严格校验通过的 `{"interrupt":true}` 能确认打断。旧音频已经播放时至少经历 120ms duck 再硬取消；尚未播放时可直接取消生成。
3. 确认后停止旧轮下行，取消旧 LLM/TTS、清空待播，并立即分配新轮次进入 `listening`；新句 final 到达后才开始新回答。浏览器使用同一条音轨。
4. `{"interrupt":false}` 恢复音量，完整 final 进入缓存。当前回答的音频发送完后，按输入顺序自动将缓存文本提交为后续问题。partial 只维护同一句的最新草稿，false 的 partial 仍会在 final 到达时重新判定。
5. 只有噪声而没有判定结果时，VAD 检测到约 500ms 静音后再等待 800ms，随后恢复正常音量。连续噪声最多 duck 4 秒，重复的开始事件不会无限延长窗口。

恢复检查使用现有 20ms RTP 时钟，旧轮次没有独立恢复定时器。手动停止、断开或新语音确认后，旧音频不会在超时后复活。ASR 失败或结束时立即解除未确认的 duck，并禁用后续 VAD 降音，直到识别服务重新可用。降音和等待新句 final 期间页面“停止回答”仍可用。打断处理不调用前端 `pause()`。

### 意图 Prompt 与缓存

[interruption.md](internal/llm/prompts/interruption.md) 随 Go 二进制嵌入，由现有 DeepSeek V4 Pro 独立调用，修改 prompt 后需重新构建或重启 `go run`。输入是结构化的上一问题、当前生成的回答、`current_tool`、最新 ASR 文本及 final 标志。直接要求与当前不同的农务动作默认 true，无需停止措辞；从问答或贴贴转向农务也适用。明确说稍后或做完再做则 false；同一动作及其同义表达、普通问题、夸赞和引用指令也 false。转入通用问答必须明确要求停止、暂停或优先回答，例如“先别种菜了，回答我...”或“先回答我的问题”，不要求固定口令。它是文本语义分类，不含声纹和声学回声判断，仍可能误判。

“立即切换”指确认后不等旧动作结束：稳定且完整的 partial 可以提前判定，final 负责补齐新任务，仍经过模型校验和最小 duck 窗口。专项回归命令为 `go run ./cmd/verify-home -suite switch`、`go run ./cmd/demo-fixtures -scene home-switch`、`node scripts/verify_home.cjs --switch`。

判定采用非思考模式、`temperature=0`、最多 32 个输出 token 和 JSON mode。服务端只接受唯一 `interrupt` 字段且值为 JSON 布尔量：`{"interrupt":true}` 或 `{"interrupt":false}`；大写 `True`、字符串、null、额外字段、重复键、解释文字、截断输出全部拒绝。判断输出不会送入 TTS。

同一句最多 3 次 partial 请求，两次发起至少间隔 500ms；final 不受该限流影响。每句同一时间只保留一个有效请求，final、ASR 改写或追加文字都会取消过时请求；最小 duck 窗口中尚未实施的确认同样可以撤回。每次请求最长 2 秒，超时、非法输出或请求失败均显式兜底为 `interrupt=false`，保留旧播报并缓存 final。结果绑定原轮次和请求版本，旧轮完成或被取消后，迟到的 true 不能打断新轮。日志记录 `is_final` 与 `utterance_id`，可区分使用了草稿还是完整文本。

兜底的 `intent_result` 带有 `interrupt:false`、`fallback:true`、`status:"error"` 和原因 `invalid_output`、`timeout` 或 `request_failed`。服务端日志显式记录相同字段及耗时和安全错误描述；模型正常返回 false 时，日志为 `interrupt=false fallback=false`。严格 JSON 校验仍然执行，多余文字不会被截取后当作模型结果使用。

缓存最多 8 句，每句最多 2000 字符，按首次收到该句的顺序处理。重复 final 去重；未完成的 partial 连续 15 秒无更新或 ASR 结束时释放。队列满会明确提示本句未收录。手动停止、断开或语音确认打断都会清空待处理输入；语音打断只保留触发本次切换的新指令，旧缓存和草稿作废，相关意图请求取消，迟到 final 不会重新入队。清空发生在实际执行打断时，疑似插话、false 和被撤回的确认不清空。新轮期间新收到的输入仍按正常规则处理。false 表示延后处理，普通“嗯”等完整转写也会被保留并在播完后提交。

日志 `input_queue ... cleared=true reason=interrupted inputs_dropped=N` 记录丢弃的待处理输入数（含草稿），页面收到 `input_queue` 的零计数。清缓存语音回归使用 `go run ./cmd/demo-fixtures -scene home-switch` 和 `node scripts/verify_home.cjs --clear-buffer`。

“播完”目前按服务端生成完成且待播帧全部发送判断，不是物理耳机播放结束的精确回调。若判定仍在运行时旧轮已结束，取消这个已经没有必要的判定，直接按队列处理完整文本。

能量 VAD 无法可靠区分咳嗽、回声和语音；若 ASR 把噪声误转写成停止请求，语义模型也可能误确认。早期联调观察到单频测试音被转写，宽带噪声的无文本恢复路径已验证。已经发往浏览器抖动缓冲区的音频可能保留少量尾音，建议使用耳机。浏览器已启用回声消除请求。

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

final 的本地接收时间在缓存中保留，因此 final 到首字/首音频指标包含意图判断和主动排队时间。`intent_result.latency_ms` 单独记录意图请求耗时；页面显示判断状态与待回答数量，日志包含判定布尔值、缓存和出队事件。

协议依据：[DeepSeek 模型说明](https://api-docs.deepseek.com/quick_start/pricing)、[非思考模式参数](https://api-docs.deepseek.com/guides/thinking_mode)、[腾讯流式语音合成 WebSocket](https://cloud.tencent.com/document/product/1073/94308)。

## 验证

```bash
go test ./...
go vet ./...
```

本地测试覆盖 ASR 签名和流式协议、DeepSeek SSE 及取消、TTS 在 final 前输出音频、PCM16 分片边界、合成 WebSocket 关闭、分句长度、轮次替换、旧音频清理、队列限长及错误传播。可控时钟验证 50% 增益、队列持续消费、稳定 partial、final 前新轮次分配、同时活动的 LLM/TTS 取消、无文本恢复和降音写入失败重试。默认测试不调用付费云接口。装有 C 编译器并启用 CGO 的环境可另外执行 `go test -race ./internal/dialogue ./internal/llm ./internal/tts ./internal/rtc`。

意图测试覆盖 JSON 严格格式、HTTP 取消、false 保留旧音频并按序回答缓存、partial false 后 final true、final 覆盖迟到 partial、旧轮播完时仍在判定、非法输出及超时降级、缓存限长、丢失 final、手动清空和排队延迟指标。

已使用真实腾讯 ASR、DeepSeek V4 Pro、腾讯 TTS 和浏览器虚拟麦克风完成短句联调：识别字幕、生成回答、下行非零音频能量、重连和手动停止均通过。桌面及 390px 宽度页面也已检查。实际麦克风、扬声器回声及网络变化下的体验仍需在目标设备测试。

2026-09-12 流式 TTS 与两阶段 duck 联调通过：320ms 宽带噪声触发降音后同轮恢复，降音期间音频能量和上行包数仍增长；明确插话清掉旧轮 250 帧，新轮先于对应 final 约 988ms 建立。ASR 把插话分成两个 final，最终第 3 轮回答“1+1等于2”。本次噪声降音延迟 210ms，最后一句从估算句末到首字 1017ms、到首个非静音 RTP 帧 1869ms；仅为单次服务端测量，不是延迟承诺。

上述历史云联调使用 20% 增益和早期文本规则，原始证据保留当时的数值。当前为 50% 增益及独立 LLM 意图判断，新证据见 [判定样例](docs/evidence/intent-classifier.json) 和 [真实语音双路径联调](docs/evidence/intent-barge-in.json)。6 个文本样例均符合预期，单次判定 415–1154ms；不是稳定时延或准确率承诺。

复现浏览器联调需要 Node.js、Chrome、有效云凭证及已运行的服务，会实际调用云接口。原 REST `TextToVoice` 仅用于生成固定输入素材，不是运行中的 TTS 回退路径。

```powershell
E:\go\bin\go.exe run ./cmd/demo-fixtures
npm install --prefix bin/browser-check --no-save playwright
$env:NODE_PATH = (Resolve-Path 'bin/browser-check/node_modules').Path
# Chrome 不在默认位置时设置 CHROME_PATH；服务不是 8080 时设置 DEMO_URL。
E:\go\bin\go.exe run ./cmd/verify-intent
node scripts/verify_intent.cjs
```

脚本使用虚拟麦克风输入，成功后更新 `docs/evidence/intent-barge-in.json`，截图保存在 `bin/`。自动化运行静音，不代表已测得物理耳机声音。早期噪声和双向音频验证脚本 `scripts/verify_barge_in.cjs` 也保留供复现。

## 固定验收与统计

[固定验收说明](docs/验收测试说明.md)提供 60 个版本化文本样例和 5 个真实语音场景。默认每个文本重复 3 次，统计严格通过率、误打断/漏打断、请求错误、混淆矩阵和 P50/P95；语音验证取消、清缓存、首次夸赞和十次播报。异常 false 不计分类成功，失败也保存证据并返回非零退出码。

```powershell
# 需要可用的 .env、Node.js、Chrome 和 Playwright；会实际调用云接口。
$env:GO_BIN = 'E:/go/bin/go.exe'
$env:NODE_PATH = (Resolve-Path 'bin/browser-check/node_modules').Path
node scripts/verify_acceptance.cjs
# 只检查分类；不调用腾讯 ASR/TTS。
node scripts/verify_acceptance.cjs --text-only --repeat 1
```

结果位于 `bin/acceptance/<UTC时间>-<进程ID>/report.md`，同目录保留 JSON、日志、固定集快照与截图。脚本使用自动分配端口的独立测试服务，运行中的 8080 服务不受影响。Go 在 PATH 时可省略 `GO_BIN`；Playwright 首次安装方式及指标边界见说明文档。

[2026-09-12 完整基线报告](docs/evidence/acceptance-20260912/report.md)：180/180 次文本分类、5/5 个真实语音场景通过；文本动作/打断请求的 P95 分别为 1216ms / 919ms。这是固定输入回归，不代表真人噪声、回声或物理耳机时延已验收。

## 下一步

补充真实耳机/扬声器回声、咳嗽和短暂停顿场景，评估断句拆分及误触发，并测量浏览器端实际播放延迟。
