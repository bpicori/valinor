package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go/v3"
	"valinor/config"
)

func main() {
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	client := cfg.NewClient()

	fmt.Println("Calling API with model: ", cfg.Model)
	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Who are you?"),
		},
	})
	if err != nil {
		log.Fatalf("chat completion: %v", err)
	}

	fmt.Println(completion.Choices[0].Message.Content)
}
