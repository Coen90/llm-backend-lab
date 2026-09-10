package llm

// responseRequest is the JSON body sent to OpenAI's Responses API.
type responseRequest struct {
	Model           string          `json:"model"`
	Input           string          `json:"input"`
	Store           bool            `json:"store"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Reasoning       reasoningConfig `json:"reasoning"`
}

type reasoningConfig struct {
	Effort string `json:"effort"`
}

// responseBody contains the fields this client reads from the API response.
type responseBody struct {
	Status string       `json:"status"`
	Usage  Usage        `json:"usage"`
	Output []outputItem `json:"output"`
}

type outputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content []outputContent `json:"content"`
}

type outputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Result is the simplified response returned to the chat handler.
type Result struct {
	Answer string `json:"answer"`
	Usage  Usage  `json:"usage"`
}
