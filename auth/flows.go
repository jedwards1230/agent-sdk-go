package auth

// defaultFlows registers the built-in OAuth login flows keyed by provider id.
func defaultFlows() map[string]loginFlow {
	return map[string]loginFlow{
		anthropicProvider: newAnthropicFlow(),
		openaiProvider:    newOpenAIFlow(),
	}
}
