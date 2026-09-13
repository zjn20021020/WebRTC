package asr

import (
	"errors"
	"fmt"
)

// Keep provider messages and signed URLs out of errors, logs and browser events.
type ProviderError struct {
	Code int
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("Tencent ASR rejected the request (code %d)", e.Code)
}

func FailureEvent(err error, model string) Event {
	event := Event{Event: "asr_error", Status: "failed", Model: model,
		Detail: "识别连接中断，请断开后重新连接；若仍失败，请检查网络与服务端日志。"}
	if errors.Is(err, ErrAudioBacklog) {
		event.Status, event.Detail = "backlog", "识别音频积压，请检查网络后断开重连。"
		return event
	}
	var provider *ProviderError
	if !errors.As(err, &provider) {
		return event
	}
	event.Code = provider.Code
	var detail string
	switch provider.Code {
	case 4001:
		event.Status, detail = "invalid_request", "识别参数不合法，请检查服务端引擎和音频配置。"
	case 4002:
		event.Status, detail = "auth_failed", "识别鉴权失败，请核对腾讯云凭证、调用权限和系统时间。"
	case 4003:
		event.Status, detail = "service_unavailable", "当前账号尚未开通对应识别服务，请在腾讯云控制台开通。"
	case 4004:
		event.Status, detail = "quota_exhausted", "当前引擎没有可用识别额度。普通实时识别、大模型 1.0 和 2.0 资源包分别计费，请核对资源包与引擎是否匹配。修正后断开重连。"
	case 4005:
		event.Status, detail = "account_overdue", "腾讯云账号欠费停服，请处理欠费后断开重连。"
	case 4006:
		event.Status, detail = "concurrency_limit", "识别并发已达上限，请关闭其他识别会话后重连。"
	case 4007:
		detail = "识别音频解码失败，请检查音频格式与采样率配置。"
	case 6001:
		detail = "识别请求被判定为境外调用，请核对代理出口及跨境服务权限。"
	default:
		detail = "识别服务返回错误，请断开重连；若仍失败，请按错误码核对腾讯云服务状态。"
	}
	event.Detail = fmt.Sprintf("腾讯 ASR %d（%s）：%s", provider.Code, model, detail)
	return event
}
