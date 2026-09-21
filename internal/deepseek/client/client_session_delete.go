package client

import (
	"context"
	dsprotocol "ds2api/internal/deepseek/protocol"
	"errors"
	"fmt"
	"net/http"

	"ds2api/internal/auth"
	"ds2api/internal/config"
)

// DeleteSessionResult 删除会话结果
type DeleteSessionResult struct {
	SessionID    string // 会话 ID
	Success      bool   // 是否成功
	ErrorMessage string // 错误信息
}

// DeleteSession 删除单个会话。Policy-free：鉴权 / 429 拒绝返回类型化
// *RequestFailure 交给共享失败策略（internal/completionruntime），不在此处
// 刷新 token 或切换账号；网络错误与未知失败按 maxAttempts 同账号重试。
func (c *Client) DeleteSession(ctx context.Context, a *auth.RequestAuth, sessionID string, maxAttempts int) (*DeleteSessionResult, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	clients := c.requestClientsForAuth(ctx, a)

	result := &DeleteSessionResult{
		SessionID: sessionID,
	}

	if sessionID == "" {
		result.ErrorMessage = "session_id is required"
		return result, errors.New(result.ErrorMessage)
	}

	attempts := 0

	for attempts < maxAttempts {
		headers := c.authHeaders(a.DeepSeekToken)

		payload := map[string]any{
			"chat_session_id": sessionID,
		}

		resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekDeleteSessionURL, headers, payload)
		if err != nil {
			config.Logger.Warn("[delete_session] request error", "error", err, "session_id", sessionID)
			attempts++
			continue
		}

		code, bizCode, msg, bizMsg := extractResponseStatus(resp)
		if status == http.StatusOK && code == 0 && bizCode == 0 {
			result.Success = true
			return result, nil
		}

		result.ErrorMessage = fmt.Sprintf("status=%d, code=%d, msg=%s", status, code, msg)
		config.Logger.Warn("[delete_session] failed", "status", status, "code", code, "biz_code", bizCode, "msg", msg, "biz_msg", bizMsg, "session_id", sessionID)

		if failure := classifyResponseFailure("delete session", status, code, bizCode, msg, bizMsg, a.UseConfigToken); failure != nil {
			result.Success = false
			result.ErrorMessage = failure.Error()
			return result, failure
		}
		attempts++
	}

	result.Success = false
	result.ErrorMessage = "delete session failed after retries"
	return result, errors.New(result.ErrorMessage)
}

// DeleteSessionForToken 直接使用 token 删除会话（直通模式）
func (c *Client) DeleteSessionForToken(ctx context.Context, token string, sessionID string) (*DeleteSessionResult, error) {
	clients := c.requestClientsFromContext(ctx)
	result := &DeleteSessionResult{
		SessionID: sessionID,
	}

	if sessionID == "" {
		result.ErrorMessage = "session_id is required"
		return result, errors.New(result.ErrorMessage)
	}

	headers := c.authHeaders(token)
	payload := map[string]any{
		"chat_session_id": sessionID,
	}

	resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekDeleteSessionURL, headers, payload)
	if err != nil {
		result.ErrorMessage = err.Error()
		return result, err
	}

	code := intFrom(resp["code"])
	if status != http.StatusOK || code != 0 {
		msg, _ := resp["msg"].(string)
		result.ErrorMessage = fmt.Sprintf("request failed: status=%d, code=%d, msg=%s", status, code, msg)
		return result, errors.New(result.ErrorMessage)
	}

	result.Success = true
	return result, nil
}

// DeleteAllSessions 删除所有会话（谨慎使用）
func (c *Client) DeleteAllSessions(ctx context.Context, a *auth.RequestAuth) error {
	clients := c.requestClientsForAuth(ctx, a)
	headers := c.authHeaders(a.DeepSeekToken)
	payload := map[string]any{}

	resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekDeleteAllSessionsURL, headers, payload)
	if err != nil {
		config.Logger.Warn("[delete_all_sessions] request error", "error", err)
		return err
	}

	code := intFrom(resp["code"])
	if status != http.StatusOK || code != 0 {
		msg, _ := resp["msg"].(string)
		config.Logger.Warn("[delete_all_sessions] failed", "status", status, "code", code, "msg", msg)
		return fmt.Errorf("request failed: status=%d, code=%d, msg=%s", status, code, msg)
	}

	return nil
}

// DeleteAllSessionsForToken 直接使用 token 删除所有会话（直通模式）
func (c *Client) DeleteAllSessionsForToken(ctx context.Context, token string) error {
	clients := c.requestClientsFromContext(ctx)
	headers := c.authHeaders(token)
	payload := map[string]any{}

	resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekDeleteAllSessionsURL, headers, payload)
	if err != nil {
		config.Logger.Warn("[delete_all_sessions_for_token] request error", "error", err)
		return err
	}

	code := intFrom(resp["code"])
	if status != http.StatusOK || code != 0 {
		msg, _ := resp["msg"].(string)
		config.Logger.Warn("[delete_all_sessions_for_token] failed", "status", status, "code", code, "msg", msg)
		return fmt.Errorf("request failed: status=%d, code=%d, msg=%s", status, code, msg)
	}

	return nil
}
