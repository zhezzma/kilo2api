package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

type OpenAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Stream      bool                `json:"stream"`
	Messages    []OpenAIChatMessage `json:"messages"`
	MaxTokens   int                 `json:"max_tokens"`
	Temperature float64             `json:"temperature"`
}

type OpenAIChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// 修正后的Claude请求结构
type ClaudeCompletionRequest struct {
	Model       string                `json:"model"`
	MaxTokens   int                   `json:"max_tokens"`
	Temperature float64               `json:"temperature"`
	System      []ClaudeSystemMessage `json:"system,omitempty"`
	Messages    []ClaudeMessage       `json:"messages,omitempty"`
	Stream      bool                  `json:"stream,omitempty"`
}

// 修正后的Claude系统消息结构，添加了Type字段
type ClaudeSystemMessage struct {
	Type string `json:"type"` // 添加type字段
	Text string `json:"text"`
}

type ClaudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// ConvertOpenAIToClaudeRequest 将OpenAI请求转换为Claude请求的函数
func ConvertOpenAIToClaudeRequest(openAIReq OpenAIChatCompletionRequest) (ClaudeCompletionRequest, error) {
	claudeReq := ClaudeCompletionRequest{
		Model:       openAIReq.Model, // 使用Claude模型
		MaxTokens:   openAIReq.MaxTokens,
		Temperature: openAIReq.Temperature, // 默认温度设为0
		Stream:      true,                  // 保留stream设置
	}

	// 处理消息
	var systemMessages []ClaudeSystemMessage
	var claudeMessages []ClaudeMessage

	for _, msg := range openAIReq.Messages {
		if msg.Role == "system" {
			// 将system消息转换为Claude的system格式
			textContent, ok := msg.Content.(string)
			if !ok {
				// 如果不是字符串，尝试将其转换为JSON字符串
				contentBytes, err := json.Marshal(msg.Content)
				if err != nil {
					return claudeReq, fmt.Errorf("无法序列化system消息内容: %v", err)
				}
				textContent = string(contentBytes)
			}
			// 添加type字段，设置为"text"
			systemMessages = append(systemMessages, ClaudeSystemMessage{
				Type: "text",
				Text: textContent,
			})
		} else {
			// 用户和助手消息
			claudeRole := msg.Role
			if msg.Role == "assistant" {
				claudeRole = "assistant"
			} else if msg.Role == "user" {
				claudeRole = "user"
			}

			claudeMessages = append(claudeMessages, ClaudeMessage{
				Role:    claudeRole,
				Content: msg.Content,
			})
		}
	}

	claudeReq.System = systemMessages
	claudeReq.Messages = claudeMessages

	return claudeReq, nil
}
func (r *OpenAIChatCompletionRequest) AddMessage(message OpenAIChatMessage) {
	r.Messages = append([]OpenAIChatMessage{message}, r.Messages...)
}

func (r *OpenAIChatCompletionRequest) PrependMessagesFromJSON(jsonString string) error {
	var newMessages []OpenAIChatMessage
	err := json.Unmarshal([]byte(jsonString), &newMessages)
	if err != nil {
		return err
	}

	// 查找最后一个 system role 的索引
	var insertIndex int
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "system" {
			insertIndex = i + 1
			break
		}
	}

	// 将 newMessages 插入到找到的索引后面
	r.Messages = append(r.Messages[:insertIndex], append(newMessages, r.Messages[insertIndex:]...)...)
	return nil
}

func (r *OpenAIChatCompletionRequest) SystemMessagesProcess(model string) {
	if r.Messages == nil {
		return
	}

	for i := range r.Messages {
		if r.Messages[i].Role == "system" {
			r.Messages[i].Role = "user"
		}

	}

}

func (r *OpenAIChatCompletionRequest) FilterUserMessage() {
	if r.Messages == nil {
		return
	}

	// 返回最后一个role为user的元素
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			r.Messages = r.Messages[i:]
			break
		}
	}
}

type OpenAIErrorResponse struct {
	OpenAIError OpenAIError `json:"error"`
}

type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

type OpenAIChatCompletionResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []OpenAIChoice `json:"choices"`
	Usage             OpenAIUsage    `json:"usage"`
	SystemFingerprint *string        `json:"system_fingerprint"`
	Suggestions       []string       `json:"suggestions"`
}

type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	LogProbs     *string       `json:"logprobs"`
	FinishReason *string       `json:"finish_reason"`
	Delta        OpenAIDelta   `json:"delta"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIDelta struct {
	Content string `json:"content"`
	Role    string `json:"role"`
}

type OpenAIImagesGenerationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	ResponseFormat string `json:"response_format"`
	Image          string `json:"image"`
}

type OpenAIImagesGenerationResponse struct {
	Created     int64                                 `json:"created"`
	DailyLimit  bool                                  `json:"dailyLimit"`
	Data        []*OpenAIImagesGenerationDataResponse `json:"data"`
	Suggestions []string                              `json:"suggestions"`
}

type OpenAIImagesGenerationDataResponse struct {
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
	B64Json       string `json:"b64_json"`
}

type OpenAIGPT4VImagesReq struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type GetUserContent interface {
	GetUserContent() []string
}

type OpenAIModerationRequest struct {
	Input string `json:"input"`
}

type OpenAIModerationResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Flagged        bool               `json:"flagged"`
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

type OpenaiModelResponse struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	//Created time.Time `json:"created"`
	//OwnedBy string    `json:"owned_by"`
}

// ModelList represents a list of models.
type OpenaiModelListResponse struct {
	Object string                `json:"object"`
	Data   []OpenaiModelResponse `json:"data"`
}

func (r *OpenAIChatCompletionRequest) GetUserContent() []string {
	var userContent []string

	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			switch contentObj := r.Messages[i].Content.(type) {
			case string:
				userContent = append(userContent, contentObj)
			}
			break
		}
	}

	return userContent
}
func (r *OpenAIChatCompletionRequest) GetPreviousMessagePair() (string, bool, error) {
	messages := r.Messages
	if len(messages) < 3 {
		return "", false, nil
	}

	if len(messages) > 0 && messages[len(messages)-1].Role != "user" {
		return "", false, nil
	}

	for i := len(messages) - 2; i > 0; i-- {
		if messages[i].Role == "assistant" {
			if messages[i-1].Role == "user" {
				// 深拷贝消息对象避免污染原始数据
				prevPair := []OpenAIChatMessage{
					messages[i-1], // 用户消息
					messages[i],   // 助手消息
				}

				jsonData, err := json.Marshal(prevPair)
				if err != nil {
					return "", false, err
				}

				// 移除JSON字符串中的转义字符
				cleaned := strings.NewReplacer(
					`\n`, "",
					`\t`, "",
					`\r`, "",
				).Replace(string(jsonData))

				return cleaned, true, nil
			}
		}
	}
	return "", false, nil
}

func (r *OpenAIChatCompletionRequest) RemoveEmptyContentMessages() *OpenAIChatCompletionRequest {
	if r == nil || len(r.Messages) == 0 {
		return r
	}

	var filteredMessages []OpenAIChatMessage
	for _, msg := range r.Messages {
		// Check if content is nil
		if msg.Content == nil {
			continue
		}

		// Check if content is an empty string
		if strContent, ok := msg.Content.(string); ok && strContent == "" {
			continue
		}

		// Check if content is an empty slice
		if sliceContent, ok := msg.Content.([]interface{}); ok && len(sliceContent) == 0 {
			continue
		}

		// If we get here, the content is not empty
		filteredMessages = append(filteredMessages, msg)
	}

	r.Messages = filteredMessages
	return r
}
