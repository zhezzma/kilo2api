package kilo_api

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"kilo2api/common/config"
	logger "kilo2api/common/loggger"
	"kilo2api/cycletls"
	"strings"
)

const (
	baseURL      = "https://kilocode.ai"
	chatEndpoint = baseURL + "/api/claude/v1/messages"
)

func MakeStreamChatRequest(c *gin.Context, client cycletls.CycleTLS, jsonData []byte, cookie string) (<-chan cycletls.SSEResponse, error) {
	split := strings.Split(cookie, "=")
	if len(split) >= 2 {
		cookie = split[0]
	}
	options := cycletls.Options{
		Timeout: 10 * 60 * 60,
		Proxy:   config.ProxyUrl, // 在每个请求中设置代理
		Body:    string(jsonData),
		Method:  "POST",
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"Accept":            "application/json",
			"Host":              "kilocode.ai",
			"anthropic-version": "2023-06-01",
			"authorization":     "Bearer " + cookie,
			"User-Agent":        config.UserAgent,
		},
	}

	logger.Debug(c.Request.Context(), fmt.Sprintf("cookie: %v", cookie))

	sseChan, err := client.DoSSE(chatEndpoint, options, "POST")
	if err != nil {
		logger.Errorf(c, "Failed to make stream request: %v", err)
		return nil, fmt.Errorf("Failed to make stream request: %v", err)
	}
	return sseChan, nil
}
