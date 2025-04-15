package controller

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"io"
	"kilo2api/common"
	"kilo2api/common/config"
	logger "kilo2api/common/loggger"
	"kilo2api/cycletls"
	"kilo2api/kilo-api"
	"kilo2api/model"
	"net/http"
	"strings"
	"time"
)

const (
	errServerErrMsg  = "Service Unavailable"
	responseIDFormat = "chatcmpl-%s"
)

// ChatForOpenAI @Summary OpenAI对话接口
// @Description OpenAI对话接口
// @Tags OpenAI
// @Accept json
// @Produce json
// @Param req body model.OpenAIChatCompletionRequest true "OpenAI对话请求"
// @Param Authorization header string true "Authorization API-KEY"
// @Router /v1/chat/completions [post]
func ChatForOpenAI(c *gin.Context) {
	client := cycletls.Init()
	defer safeClose(client)

	var openAIReq model.OpenAIChatCompletionRequest
	if err := c.BindJSON(&openAIReq); err != nil {
		logger.Errorf(c.Request.Context(), err.Error())
		c.JSON(http.StatusInternalServerError, model.OpenAIErrorResponse{
			OpenAIError: model.OpenAIError{
				Message: "Invalid request parameters",
				Type:    "request_error",
				Code:    "500",
			},
		})
		return
	}

	openAIReq.RemoveEmptyContentMessages()

	modelInfo, b := common.GetModelInfo(openAIReq.Model)
	if !b {
		c.JSON(http.StatusBadRequest, model.OpenAIErrorResponse{
			OpenAIError: model.OpenAIError{
				Message: fmt.Sprintf("Model %s not supported", openAIReq.Model),
				Type:    "invalid_request_error",
				Code:    "invalid_model",
			},
		})
		return
	}
	if openAIReq.MaxTokens > modelInfo.MaxTokens {
		c.JSON(http.StatusBadRequest, model.OpenAIErrorResponse{
			OpenAIError: model.OpenAIError{
				Message: fmt.Sprintf("Max tokens %d exceeds limit %d", openAIReq.MaxTokens, modelInfo.MaxTokens),
				Type:    "invalid_request_error",
				Code:    "invalid_max_tokens",
			},
		})
		return
	}

	if openAIReq.Stream {
		handleStreamRequest(c, client, openAIReq, modelInfo)
	} else {
		handleNonStreamRequest(c, client, openAIReq, modelInfo)
	}
}

func handleNonStreamRequest(c *gin.Context, client cycletls.CycleTLS, openAIReq model.OpenAIChatCompletionRequest, modelInfo common.ModelInfo) {
	ctx := c.Request.Context()
	cookieManager := config.NewCookieManager()
	maxRetries := len(cookieManager.Cookies)
	cookie, err := cookieManager.GetRandomCookie()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		requestBody, err := createRequestBody(c, &openAIReq, modelInfo)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to marshal request body"})
			return
		}
		sseChan, err := kilo_api.MakeStreamChatRequest(c, client, jsonData, cookie, modelInfo)
		if err != nil {
			logger.Errorf(ctx, "MakeStreamChatRequest err on attempt %d: %v", attempt+1, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		isRateLimit := false
		var delta string
		var assistantMsgContent string
		var shouldContinue bool
		thinkStartType := new(bool)
		thinkEndType := new(bool)
	SSELoop:
		for response := range sseChan {
			data := response.Data
			if data == "" {
				continue
			}
			if response.Done {
				switch {
				case common.IsUsageLimitExceeded(data):
					if config.CheatEnabled {
						split := strings.Split(cookie, "=")
						if len(split) == 2 {
							cookieSession := split[1]
							cheatResp, err := client.Do(config.CheatUrl, cycletls.Options{
								Timeout: 10 * 60 * 60,
								Proxy:   config.ProxyUrl, // 在每个请求中设置代理
								Body:    "",
								Headers: map[string]string{
									"Cookie": cookieSession,
								},
							}, "POST")
							if err != nil {
								logger.Errorf(ctx, "Cheat err Cookie: %s err: %v", cookie, err)
								c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
								return
							}
							if cheatResp.Status == 200 {
								logger.Debug(c, fmt.Sprintf("Cheat Success Cookie: %s", cookie))
								attempt-- // 抵消循环结束时的attempt++
								break SSELoop
							}
							if cheatResp.Status == 402 {
								logger.Warnf(ctx, "Cookie Unlink Card Cookie: %s", cookie)
							} else {
								logger.Errorf(ctx, "Cheat err Cookie: %s Resp: %v", cookie, cheatResp.Body)
								c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Cheat Resp.Status:%v Resp.Body:%v", cheatResp.Status, cheatResp.Body)})
								return
							}
						}
					}

					isRateLimit = true
					logger.Warnf(ctx, "Cookie Usage limit exceeded, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
					config.RemoveCookie(cookie)
					break SSELoop
				case common.IsServerError(data):
					logger.Errorf(ctx, errServerErrMsg)
					c.JSON(http.StatusInternalServerError, gin.H{"error": errServerErrMsg})
					return
				case common.IsNotLogin(data):
					isRateLimit = true
					logger.Warnf(ctx, "Cookie Not Login, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
					break SSELoop
				case common.IsRateLimit(data):
					isRateLimit = true
					logger.Warnf(ctx, "Cookie rate limited, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
					config.AddRateLimitCookie(cookie, time.Now().Add(time.Duration(config.RateLimitCookieLockDuration)*time.Second))
					break SSELoop
				}
				logger.Warnf(ctx, response.Data)
				return
			}

			logger.Debug(ctx, strings.TrimSpace(data))

			streamDelta, streamShouldContinue := processNoStreamData(c, data, modelInfo, thinkStartType, thinkEndType)
			delta = streamDelta
			shouldContinue = streamShouldContinue
			// 处理事件流数据
			if !shouldContinue {
				promptTokens := model.CountTokenText(string(jsonData), openAIReq.Model)
				completionTokens := model.CountTokenText(assistantMsgContent, openAIReq.Model)
				finishReason := "stop"

				c.JSON(http.StatusOK, model.OpenAIChatCompletionResponse{
					ID:      fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405")),
					Object:  "chat.completion",
					Created: time.Now().Unix(),
					Model:   openAIReq.Model,
					Choices: []model.OpenAIChoice{{
						Message: model.OpenAIMessage{
							Role:    "assistant",
							Content: assistantMsgContent,
						},
						FinishReason: &finishReason,
					}},
					Usage: model.OpenAIUsage{
						PromptTokens:     promptTokens,
						CompletionTokens: completionTokens,
						TotalTokens:      promptTokens + completionTokens,
					},
				})

				return
			} else {
				assistantMsgContent = assistantMsgContent + delta
			}
		}
		if !isRateLimit {
			return
		}

		// 获取下一个可用的cookie继续尝试
		cookie, err = cookieManager.GetNextCookie()
		if err != nil {
			logger.Errorf(ctx, "No more valid cookies available after attempt %d", attempt+1)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

	}
	logger.Errorf(ctx, "All cookies exhausted after %d attempts", maxRetries)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "All cookies are temporarily unavailable."})
	return
}

func createRequestBody(c *gin.Context, openAIReq *model.OpenAIChatCompletionRequest, modelInfo common.ModelInfo) (map[string]interface{}, error) {

	client := cycletls.Init()
	defer safeClose(client)

	if config.PRE_MESSAGES_JSON != "" {
		err := openAIReq.PrependMessagesFromJSON(config.PRE_MESSAGES_JSON)
		if err != nil {
			return nil, fmt.Errorf("PrependMessagesFromJSON err: %v JSON:%s", err, config.PRE_MESSAGES_JSON)
		}
	}

	if openAIReq.MaxTokens <= 1 {
		openAIReq.MaxTokens = 8000
	}

	var data []byte
	var err error
	if modelInfo.Source == "claude" {
		claudeRequest, err := model.ConvertOpenAIToClaudeRequest(*openAIReq, modelInfo)
		if err != nil {
			return nil, fmt.Errorf("ConvertOpenAIToClaudeRequest err: %v", err)
		}
		data, err = json.Marshal(claudeRequest)
		if err != nil {
			return nil, err
		}

	} else if modelInfo.Source == "openrouter" {
		geminiReq, err := model.ConvertOpenAIToGeminiRequest(*openAIReq, modelInfo)
		if err != nil {
			return nil, fmt.Errorf("ConvertOpenAIToGeminiRequest err: %v", err)
		}
		data, err = json.Marshal(geminiReq)
		if err != nil {
			return nil, err
		}
	}

	requestBody := make(map[string]interface{})
	err = json.Unmarshal(data, &requestBody)
	if err != nil {
		return nil, err
	}

	// 创建请求体
	logger.Debug(c.Request.Context(), fmt.Sprintf("RequestBody: %v", requestBody))

	return requestBody, nil
}

// createStreamResponse 创建流式响应
func createStreamResponse(responseId, modelName string, jsonData []byte, delta model.OpenAIDelta, finishReason *string) model.OpenAIChatCompletionResponse {
	promptTokens := model.CountTokenText(string(jsonData), modelName)
	completionTokens := model.CountTokenText(delta.Content, modelName)
	return model.OpenAIChatCompletionResponse{
		ID:      responseId,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
		Usage: model.OpenAIUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
}

// handleDelta 处理消息字段增量
func handleDelta(c *gin.Context, delta string, responseId, modelName string, jsonData []byte) error {
	// 创建基础响应
	createResponse := func(content string) model.OpenAIChatCompletionResponse {
		return createStreamResponse(
			responseId,
			modelName,
			jsonData,
			model.OpenAIDelta{Content: content, Role: "assistant"},
			nil,
		)
	}

	// 发送基础事件
	var err error
	if err = sendSSEvent(c, createResponse(delta)); err != nil {
		return err
	}

	return err
}

// handleMessageResult 处理消息结果
func handleMessageResult(c *gin.Context, responseId, modelName string, jsonData []byte) bool {
	finishReason := "stop"
	var delta string

	promptTokens := 0
	completionTokens := 0

	streamResp := createStreamResponse(responseId, modelName, jsonData, model.OpenAIDelta{Content: delta, Role: "assistant"}, &finishReason)
	streamResp.Usage = model.OpenAIUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}

	if err := sendSSEvent(c, streamResp); err != nil {
		logger.Warnf(c.Request.Context(), "sendSSEvent err: %v", err)
		return false
	}
	c.SSEvent("", " [DONE]")
	return false
}

// sendSSEvent 发送SSE事件
func sendSSEvent(c *gin.Context, response model.OpenAIChatCompletionResponse) error {
	jsonResp, err := json.Marshal(response)
	if err != nil {
		logger.Errorf(c.Request.Context(), "Failed to marshal response: %v", err)
		return err
	}
	c.SSEvent("", " "+string(jsonResp))
	c.Writer.Flush()
	return nil
}

func handleStreamRequest(c *gin.Context, client cycletls.CycleTLS, openAIReq model.OpenAIChatCompletionRequest, modelInfo common.ModelInfo) {

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	responseId := fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405"))
	ctx := c.Request.Context()

	cookieManager := config.NewCookieManager()
	maxRetries := len(cookieManager.Cookies)
	cookie, err := cookieManager.GetRandomCookie()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	thinkStartType := new(bool)
	thinkEndType := new(bool)

	c.Stream(func(w io.Writer) bool {
		for attempt := 0; attempt < maxRetries; attempt++ {
			requestBody, err := createRequestBody(c, &openAIReq, modelInfo)
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return false
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				c.JSON(500, gin.H{"error": "Failed to marshal request body"})
				return false
			}
			sseChan, err := kilo_api.MakeStreamChatRequest(c, client, jsonData, cookie, modelInfo)
			if err != nil {
				logger.Errorf(ctx, "MakeStreamChatRequest err on attempt %d: %v", attempt+1, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return false
			}

			isRateLimit := false
		SSELoop:
			for response := range sseChan {

				if response.Status == 403 {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Forbidden"})
					return false
				}

				data := response.Data
				if data == "" {
					continue
				}

				if response.Done {
					switch {
					case common.IsUsageLimitExceeded(data):
						if config.CheatEnabled {
							split := strings.Split(cookie, "=")
							if len(split) == 2 {
								cookieSession := split[1]
								cheatResp, err := client.Do(config.CheatUrl, cycletls.Options{
									Timeout: 10 * 60 * 60,
									Proxy:   config.ProxyUrl, // 在每个请求中设置代理
									Body:    "",
									Headers: map[string]string{
										"Cookie": cookieSession,
									},
								}, "POST")
								if err != nil {
									logger.Errorf(ctx, "Cheat err Cookie: %s err: %v", cookie, err)
									c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
									return false
								}
								if cheatResp.Status == 200 {
									logger.Debug(c, fmt.Sprintf("Cheat Success Cookie: %s", cookie))
									attempt-- // 抵消循环结束时的attempt++
									break SSELoop
								}
								if cheatResp.Status == 402 {
									logger.Warnf(ctx, "Cheat failed.  Cookie: %s Resp: %v", cookie, cheatResp.Body)
								} else {
									logger.Errorf(ctx, "Cheat err Cookie: %s Resp: %v", cookie, cheatResp.Body)
									c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Cheat Resp.Status:%v Resp.Body:%v", cheatResp.Status, cheatResp.Body)})
									return false
								}
							}
						}
						isRateLimit = true
						logger.Warnf(ctx, "Cookie Usage limit exceeded, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
						config.RemoveCookie(cookie)
						break SSELoop
					case common.IsServerError(data):
						logger.Errorf(ctx, errServerErrMsg)
						c.JSON(http.StatusInternalServerError, gin.H{"error": errServerErrMsg})
						return false
					case common.IsNotLogin(data):
						isRateLimit = true
						logger.Warnf(ctx, "Cookie Not Login, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
						break SSELoop // 使用 label 跳出 SSE 循环
					case common.IsRateLimit(data):
						isRateLimit = true
						logger.Warnf(ctx, "Cookie rate limited, switching to next cookie, attempt %d/%d, COOKIE:%s", attempt+1, maxRetries, cookie)
						config.AddRateLimitCookie(cookie, time.Now().Add(time.Duration(config.RateLimitCookieLockDuration)*time.Second))
						break SSELoop
					}
					logger.Warnf(ctx, response.Data)
					return false
				}

				logger.Debug(ctx, strings.TrimSpace(data))

				_, shouldContinue := processStreamData(c, data, responseId, openAIReq.Model, modelInfo, jsonData, thinkStartType, thinkEndType)
				// 处理事件流数据

				if !shouldContinue {
					return false
				}
			}

			if !isRateLimit {
				return true
			}

			// 获取下一个可用的cookie继续尝试
			cookie, err = cookieManager.GetNextCookie()
			if err != nil {
				logger.Errorf(ctx, "No more valid cookies available after attempt %d", attempt+1)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return false
			}
		}

		logger.Errorf(ctx, "All cookies exhausted after %d attempts", maxRetries)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "All cookies are temporarily unavailable."})
		return false
	})
}

// 处理流式数据的辅助函数，返回bool表示是否继续处理
func processStreamData(c *gin.Context, data, responseId, model string, modelInfo common.ModelInfo, jsonData []byte, thinkStartType, thinkEndType *bool) (string, bool) {
	data = strings.TrimSpace(data)
	data = strings.TrimPrefix(data, "data: ")

	// 处理[DONE]标记
	if data == "[DONE]" {
		return "", false
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		logger.Errorf(c.Request.Context(), "Failed to unmarshal event: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return "", false
	}

	if modelInfo.Source == "claude" {
		eventType, ok := event["type"]
		if !ok {
			logger.Errorf(c.Request.Context(), "Event type not found")
			return "", false
		}

		if eventType == "message_stop" {
			handleMessageResult(c, responseId, model, jsonData)
			return "", false
		}

		var text string
		deltaMap, ok := event["delta"].(map[string]interface{})
		if ok {
			thinking, ok := deltaMap["thinking"].(string)
			if ok {
				if !*thinkStartType {
					text = "<think>\n\n" + thinking
					*thinkStartType = true
					*thinkEndType = false
				} else {
					text = thinking
				}
			}

			deltaText, ok := deltaMap["text"].(string)
			if ok {
				if *thinkStartType && !*thinkEndType {
					text = "</think>\n\n" + deltaText
					*thinkStartType = false
					*thinkEndType = true
				} else {
					text = deltaText
				}
			}
			if err := handleDelta(c, text, responseId, model, jsonData); err != nil {
				logger.Errorf(c.Request.Context(), "handleDelta err: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return "", false
			}
			return text, true
		}
		return "", true
	} else if modelInfo.Source == "openrouter" {
		// 检查是否有choices数组
		choices, ok := event["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			// 如果有usage信息但没有内容，可能是最后一个消息
			if _, hasUsage := event["usage"]; hasUsage {
				return "", false
			}
			logger.Errorf(c.Request.Context(), "Invalid openrouter response format: choices not found or empty")
			return "", false
		}

		// 获取第一个choice
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			logger.Errorf(c.Request.Context(), "Invalid choice format in openrouter response")
			return "", false
		}

		// 获取delta内容
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			logger.Errorf(c.Request.Context(), "Delta not found in openrouter response")
			return "", false
		}

		// 获取内容文本
		content, ok := delta["content"].(string)
		if !ok {
			// 没有内容，可能是其他类型的更新
			return "", true
		}

		// 处理文本内容
		if err := handleDelta(c, content, responseId, model, jsonData); err != nil {
			logger.Errorf(c.Request.Context(), "handleDelta err: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return "", false
		}

		// 检查是否完成 - 在处理内容后检查
		finishReason, hasFinishReason := choice["finish_reason"]
		if hasFinishReason && finishReason != nil && finishReason != "" {
			// 处理完成的消息
			handleMessageResult(c, responseId, model, jsonData)
			return content, false // 返回内容但标记为结束
		}

		return content, true
	}
	return "", false
}

func processNoStreamData(c *gin.Context, data string, modelInfo common.ModelInfo, thinkStartType *bool, thinkEndType *bool) (string, bool) {
	data = strings.TrimSpace(data)
	data = strings.TrimPrefix(data, "data: ")

	// 处理[DONE]标记
	if data == "[DONE]" {
		return "", false
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		logger.Errorf(c.Request.Context(), "Failed to unmarshal event: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return "", false
	}

	if modelInfo.Source == "claude" {
		eventType, ok := event["type"]
		if !ok {
			logger.Errorf(c.Request.Context(), "Event type not found")
			return "", false
		}

		if eventType == "message_stop" {
			return "", false
		}

		var text string
		deltaMap, ok := event["delta"].(map[string]interface{})
		if ok {
			thinking, ok := deltaMap["thinking"].(string)
			if ok {
				if !*thinkStartType {
					text = "<think>\n\n" + thinking
					*thinkStartType = true
					*thinkEndType = false
				} else {
					text = thinking
				}
			}

			deltaText, ok := deltaMap["text"].(string)
			if ok {
				if *thinkStartType && !*thinkEndType {
					text = "</think>\n\n" + deltaText
					*thinkStartType = false
					*thinkEndType = true
				} else {
					text = deltaText
				}
			}
			return text, true
		}
		return "", true
	} else if modelInfo.Source == "openrouter" {
		// 检查是否有choices数组
		choices, ok := event["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			// 如果有usage信息但没有内容，可能是最后一个消息
			if _, hasUsage := event["usage"]; hasUsage {
				return "", false
			}
			logger.Errorf(c.Request.Context(), "Invalid openrouter response format: choices not found or empty")
			return "", false
		}

		// 获取第一个choice
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			logger.Errorf(c.Request.Context(), "Invalid choice format in openrouter response")
			return "", false
		}

		// 获取delta内容
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			logger.Errorf(c.Request.Context(), "Delta not found in openrouter response")
			return "", false
		}

		// 获取内容文本
		content, ok := delta["content"].(string)
		if !ok {
			// 没有内容，可能是其他类型的更新
			return "", true
		}

		// 检查是否完成 - 在处理内容后检查
		finishReason, hasFinishReason := choice["finish_reason"]
		if hasFinishReason && finishReason != nil && finishReason != "" {
			// 处理完成的消息
			return content, false // 返回内容但标记为结束
		}

		return content, true
	}
	return "", false

}

// OpenaiModels @Summary OpenAI模型列表接口
// @Description OpenAI模型列表接口
// @Tags OpenAI
// @Accept json
// @Produce json
// @Param Authorization header string true "Authorization API-KEY"
// @Success 200 {object} common.ResponseResult{data=model.OpenaiModelListResponse} "成功"
// @Router /v1/models [get]
func OpenaiModels(c *gin.Context) {
	var modelsResp []string

	modelsResp = lo.Union(common.GetModelList())

	var openaiModelListResponse model.OpenaiModelListResponse
	var openaiModelResponse []model.OpenaiModelResponse
	openaiModelListResponse.Object = "list"

	for _, modelResp := range modelsResp {
		openaiModelResponse = append(openaiModelResponse, model.OpenaiModelResponse{
			ID:     modelResp,
			Object: "model",
		})
	}
	openaiModelListResponse.Data = openaiModelResponse
	c.JSON(http.StatusOK, openaiModelListResponse)
	return
}

func safeClose(client cycletls.CycleTLS) {
	if client.ReqChan != nil {
		close(client.ReqChan)
	}
	if client.RespChan != nil {
		close(client.RespChan)
	}
}

//
//func processUrl(c *gin.Context, client cycletls.CycleTLS, chatId, cookie string, url string) (string, error) {
//	// 判断是否为URL
//	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
//		// 下载文件
//		bytes, err := fetchImageBytes(url)
//		if err != nil {
//			logger.Errorf(c.Request.Context(), fmt.Sprintf("fetchImageBytes err  %v\n", err))
//			return "", fmt.Errorf("fetchImageBytes err  %v\n", err)
//		}
//
//		base64Str := base64.StdEncoding.EncodeToString(bytes)
//
//		finalUrl, err := processBytes(c, client, chatId, cookie, base64Str)
//		if err != nil {
//			logger.Errorf(c.Request.Context(), fmt.Sprintf("processBytes err  %v\n", err))
//			return "", fmt.Errorf("processBytes err  %v\n", err)
//		}
//		return finalUrl, nil
//	} else {
//		finalUrl, err := processBytes(c, client, chatId, cookie, url)
//		if err != nil {
//			logger.Errorf(c.Request.Context(), fmt.Sprintf("processBytes err  %v\n", err))
//			return "", fmt.Errorf("processBytes err  %v\n", err)
//		}
//		return finalUrl, nil
//	}
//}
//
//func fetchImageBytes(url string) ([]byte, error) {
//	resp, err := http.Get(url)
//	if err != nil {
//		return nil, fmt.Errorf("http.Get err: %v\n", err)
//	}
//	defer resp.Body.Close()
//
//	return io.ReadAll(resp.Body)
//}
//
//func processBytes(c *gin.Context, client cycletls.CycleTLS, chatId, cookie string, base64Str string) (string, error) {
//	// 检查类型
//	fileType := common.DetectFileType(base64Str)
//	if !fileType.IsValid {
//		return "", fmt.Errorf("invalid file type %s", fileType.Extension)
//	}
//	signUrl, err := kilo-api.GetSignURL(client, cookie, chatId, fileType.Extension)
//	if err != nil {
//		logger.Errorf(c.Request.Context(), fmt.Sprintf("GetSignURL err  %v\n", err))
//		return "", fmt.Errorf("GetSignURL err: %v\n", err)
//	}
//
//	err = kilo-api.UploadToS3(client, signUrl, base64Str, fileType.MimeType)
//	if err != nil {
//		logger.Errorf(c.Request.Context(), fmt.Sprintf("UploadToS3 err  %v\n", err))
//		return "", err
//	}
//
//	u, err := url.Parse(signUrl)
//	if err != nil {
//		return "", err
//	}
//
//	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path), nil
//}
