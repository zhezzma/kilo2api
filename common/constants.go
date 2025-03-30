package common

import "time"

var StartTime = time.Now().Unix() // unit: second
var Version = "v1.0.8"            // this hard coding will be replaced automatically when building, no need to manually change

type ModelInfo struct {
	Model     string
	MaxTokens int
}

// 创建映射表（假设用 model 名称作为 key）
var ModelRegistry = map[string]ModelInfo{
	"claude-3-7-sonnet-20250219": {"claude-3-7-sonnet-20250219", 64000},
}

// 通过 model 名称查询的方法
func GetModelInfo(modelName string) (ModelInfo, bool) {
	info, exists := ModelRegistry[modelName]
	return info, exists
}

func GetModelList() []string {
	var modelList []string
	for k := range ModelRegistry {
		modelList = append(modelList, k)
	}
	return modelList
}
