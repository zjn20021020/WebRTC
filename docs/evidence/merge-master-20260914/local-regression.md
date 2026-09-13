# 主分支合并前本地检查

日期：2026-09-14（Asia/Shanghai）。应用源码基线为 `7ad11ef4419fbd8b01b61f5a76f30fab517c408a`；本次修改 README 运行分支与交付文档，应用代码保持该基线。环境为 Windows amd64、Go 1.27.1、Node.js 24.16.0。

## 命令与结果

在项目根目录执行：

```powershell
& 'E:/go/bin/go.exe' test -count=1 ./...
& 'E:/go/bin/go.exe' vet ./...
& 'E:/Node/node.exe' --test scripts/acceptance_stats.test.cjs
git diff --check
```

以上命令退出码均为 0。Go 重新执行结果如下，命令入口的 `[no test files]` 不计入通过包数：

```text
ok  webrtc-interrupt/internal/asr        1.147s
ok  webrtc-interrupt/internal/audio      0.292s
ok  webrtc-interrupt/internal/dialogue   0.979s
ok  webrtc-interrupt/internal/home       0.247s
ok  webrtc-interrupt/internal/interrupt  0.275s
ok  webrtc-interrupt/internal/llm        0.780s
ok  webrtc-interrupt/internal/rtc        0.501s
ok  webrtc-interrupt/internal/signaling  0.467s
ok  webrtc-interrupt/internal/tts        0.693s
```

`go vet` 无诊断。Node 统计测试为 7 pass / 0 fail，覆盖补充关系独立统计、有序计划、planned 延迟分组、分位数与空值、错误 false 兜底、误判分母、媒体快照去重及缓存分组。

文档相对文件链接已核对存在。当前 JSON 验收集为 `home-acceptance-v4-continuation`，32 个规划、52 个打断、14 个补充关系，共 98 例；`home-media-v4-continuation` 定义 12 个媒体场景。数量来自结构化解析，不是本次云端执行次数。

## 范围

本次没有重新调用 ASR、LLM 或 TTS，没有新增真人录音、外放回声或耳机尾音测试，也未运行 race 检测。开发阶段的云端结果及各自源码摘要保留在原证据目录，参见[测试情况与证据链分析](../../测试情况与证据链分析.md#51-分版本结果)。
