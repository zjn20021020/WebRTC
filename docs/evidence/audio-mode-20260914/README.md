# 交付 3 音频模式验证记录

日期：2026-09-14。分支：`codex/aec-headphone-priority`。测试对象为本次手动耳机/扬声器兜底改动；服务使用自动路由基线后端，实时读取当前 `web/`。

| 记录 | 来源与含义 |
| --- | --- |
| routing.json | `scripts/verify_audio_routing.cjs` 实际输出；合成麦克风、模拟设备信息、浏览器内 WebRTC 对端；cloud_calls=false、physical_echo_tested=false |
| capture-stress.json | `scripts/verify_capture.cjs` 实际输出；持续合成音频、1800 个事件、GC 和挂起恢复 |
| ui-desktop.png / ui-mobile.png | `scripts/verify_ui.cjs` 生成的 1440 / 320 像素截图；另检查 1024、768、390 像素 |

本地执行 `E:/go/bin/go.exe test ./...` 返回 0，asr、audio、dialogue、home、interrupt、llm、rtc、signaling、tts 九个含测试包均通过并复用缓存；`go vet ./...` 返回 0。`node --test scripts/audio_route.test.cjs` 六项通过。结果未计入云端准确率，也未执行本次物理外放测试。

源码以交付 3 的 code 目录及其 code.zip 为准，交付说明标识提交。历史任务视频沿用旧版，不能证明本版回声消除有效。
