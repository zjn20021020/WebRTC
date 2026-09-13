# 洛克王国家园管理助手

## 场景

本项目的场景是《洛克王国：世界》的家园管理助手。按场景设定，玩家可以在家园驻派一只精灵，负责看家护院、驱逐偷菜访客，并在玩家种下种子后帮忙浇水。希望在驻派精灵的基础上接入智能助手，让玩家能直接与精灵对话、安排家园任务。

当前 Demo 固定精灵为迪莫，默认已经唤起，先实现与精灵对话的全双工语音流程。暂不实现放置精灵时的角色切换、名字检测和唤醒，也未接入真实游戏操作。

## 对话与动作

通过 `tool_call` 区分六类请求。当前使用语音模拟动作执行，除通用问答外均重复十次，包括亲密动作“贴贴”。

| 请求类型 | 当前输出 |
| --- | --- |
| 浇水 | “我正在浇水。”重复十次 |
| 种菜 | “我正在种菜。”重复十次 |
| 收菜 | “我正在收菜。”重复十次 |
| 施肥 | “我正在施肥。”重复十次 |
| 鼓励、夸赞触发的亲密动作 | “贴贴。”重复十次 |
| 通用问答 | 正常语音回答 |

语音回答过程中继续收听用户。当检测到用户说话时，先将下行音量降至 50%，再判断是否打断：确认打断后停止当前工具或对话，进入新一轮；否则将完整输入放入缓存，等当前执行结束后再处理。

示例：

1. 玩家说“迪莫，你去浇下水”，迪莫开始重复“我正在浇水。”。
2. 播报中玩家说“别浇水了，去施肥”，音量先降至 50%；确认打断后停止浇水，改为重复“我正在施肥。”。
3. 施肥中玩家说“迪莫你真棒”，音量先降至 50%；判定不打断，继续施肥并缓存夸赞，施肥结束后重复“贴贴。”十次。

## 可复现运行方式

### 环境

- Go 1.22 或以上、Git，以及支持 WebRTC 和麦克风权限的浏览器，建议 Chrome。
- 可访问腾讯云和 DeepSeek 的网络。
- 腾讯云实时语音识别 `8k_zh`、语音合成服务已开通，凭证具有调用权限且额度可用。
- 可调用 `deepseek-v4-pro` 的 DeepSeek API Key。

### 获取项目与配置

```bash
git clone https://github.com/zjn20021020/WebRTC.git
cd WebRTC
go mod download
```

在项目根目录建立 `.env`，字段与 [.env.example](.env.example) 一致，填入自己的凭证：

```dotenv
TENCENT_APP_ID=
TENCENT_SECRET_ID=
TENCENT_SECRET_KEY=
TENCENT_TTS_VOICE_TYPE=101016
DEEPSEEK_API_KEY=
DEEPSEEK_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-v4-pro
```

AppID 在[腾讯云账号信息](https://console.cloud.tencent.com/developer)中查询；SecretId、SecretKey 在同账号的 [API 密钥管理](https://console.cloud.tencent.com/cam/capi)中获取。腾讯 ASR 与 TTS 复用这三个字段。`.env` 仅供服务端读取，已被 Git 忽略，不提交密钥；系统环境变量优先于 `.env`。

### 启动与使用

在项目根目录运行：

```bash
go run ./cmd/server
```

打开 [http://localhost:8080](http://localhost:8080)，点击“连接并启用麦克风”，允许麦克风权限，然后按上面的示例对话。声音从浏览器所用的系统输出设备播放。运行本地 Demo 不需要 Node.js 或前端构建。

若 8080 已占用，可运行 `go run ./cmd/server -addr :8081`，并打开 `http://localhost:8081`。本机 localhost 可直接申请麦克风权限；远程访问需 HTTPS。修改 `.env` 后重启服务并重新连接。结束体验时点击“断开”，终端按 `Ctrl+C` 停止服务。
