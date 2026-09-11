package main

import "fmt"

type modelLimits struct {
	Context   int
	MaxOutput int
}

// Limits published by DeepSeek for the current V4 API models.
func limitsForModel(model string) (modelLimits, bool) {
	switch model {
	case "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp":
		return modelLimits{Context: 1_048_576, MaxOutput: 384_000}, true
	default:
		return modelLimits{}, false
	}
}

func lengthDiagnostic(client *DeepSeekClient, usage Usage) string {
	configuredOutput := client.MaxTokens
	limits, known := limitsForModel(client.Model)
	effectiveOutput := configuredOutput
	outputLimitName := "max_tokens"
	if known && (effectiveOutput == 0 || limits.MaxOutput < effectiveOutput) {
		effectiveOutput = limits.MaxOutput
		outputLimitName = "максимум модели"
	}

	cause := "API вернул finish_reason=length, но не передал, какой именно лимит сработал"
	if effectiveOutput > 0 && usage.Completion >= effectiveOutput {
		cause = fmt.Sprintf("достигнут лимит ответа %s=%d", outputLimitName, effectiveOutput)
	} else if known && usage.Prompt+usage.Completion >= limits.Context {
		cause = fmt.Sprintf("достигнут лимит контекста модели=%d", limits.Context)
	}

	output := fmt.Sprintf("Причина: %s. Ответ: %d/%d токенов", cause, usage.Completion, effectiveOutput)
	if known {
		output += fmt.Sprintf("; контекст: вход %d + ответ %d = %d/%d; максимум ответа модели %d",
			usage.Prompt, usage.Completion, usage.Prompt+usage.Completion, limits.Context, limits.MaxOutput)
	} else {
		output += fmt.Sprintf("; вход: %d; общий контекст: %d; лимит модели неизвестен приложению",
			usage.Prompt, usage.Prompt+usage.Completion)
	}
	return output + ". Токены учтены, незавершённый ход не сохранён"
}
