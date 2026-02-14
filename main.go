package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"valinor/agent"
	"valinor/config"
)

// toolPrefix is the prefix the agent uses for tool call logs.
const toolPrefix = "[tool] "

// cyanWriter wraps w and colors lines starting with toolPrefix in cyan.
type cyanWriter struct{ w io.Writer }

func (c cyanWriter) Write(p []byte) (n int, err error) {
	if len(p) >= len(toolPrefix) && string(p[:len(toolPrefix)]) == toolPrefix {
		const cyan, reset = "\033[36m", "\033[0m"
		_, err = c.w.Write([]byte(cyan))
		if err != nil {
			return 0, err
		}
	}
	n, err = c.w.Write(p)
	if err != nil {
		return n, err
	}
	if len(p) >= len(toolPrefix) && string(p[:len(toolPrefix)]) == toolPrefix {
		_, _ = c.w.Write([]byte("\033[0m"))
	}
	return n, nil
}

func main() {
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ag, err := agent.New(cfg.NewClient(), cfg.Model, "./agent_workspace")
	if err != nil {
		log.Fatalf("init agent: %v", err)
	}
	defer ag.Close()

	fmt.Fprintln(os.Stderr, "Valinor REPL — model:", cfg.Model)
	fmt.Fprintln(os.Stderr, "Commands: /new (start new conversation), /exit (quit)")
	fmt.Fprintln(os.Stderr)

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch line {
		case "/exit":
			fmt.Fprintln(os.Stderr, "Goodbye.")
			return
		case "/new":
			ag.NewConversation()
			fmt.Fprintln(os.Stderr, "New conversation started.")
			continue
		}

		_, usage, err := ag.Send(context.Background(), line, os.Stdout, os.Stderr, cyanWriter{os.Stderr})
		if err != nil {
			log.Fatalf("agent error: %v", err)
		}
		fmt.Println()
		if usage.TotalTokens > 0 {
			fmt.Fprintf(os.Stderr, "Usage: prompt=%d completion=%d total=%d\n",
				usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		}
	}
}
