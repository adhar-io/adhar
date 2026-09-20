/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package ai

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Hold a conversation with the platform's model",
	Long: `Open an interactive session. The conversation keeps its history, so follow-up
questions work ("why?", "show me the other one").

The session starts with the same cluster summary ` + "`adhar ai ask`" + ` uses. It does not
call tools — use ` + "`adhar ai agent`" + ` when the question needs the model to go and look.

Slash commands inside the session:
  /model <name>   switch model mid-conversation
  /context        re-attach a fresh cluster summary
  /clear          forget the conversation and start over
  /save <file>    write the transcript to a file
  /exit           leave (Ctrl-D does the same)`,
	RunE:         runChat,
	SilenceUsage: true,
}

func runChat(cmd *cobra.Command, _ []string) error {
	ctx := ensureContext(cmd.Context())
	p, err := newPlatform()
	if err != nil {
		return err
	}
	c, err := dial(ctx, p)
	if err != nil {
		return err
	}
	defer c.Close()

	msgs := []message{{Role: "system", Content: systemPrompt(ctx, p, true, nil)}}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("💬", "Adhar AI chat"))
	fmt.Printf("\n  model %s · cluster context attached · /exit to leave\n\n", c.model)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print(helpers.HighlightStyle.Render("you ▸ "))
		if !in.Scan() {
			fmt.Println()
			return nil
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			done, err := chatCommand(cmd, p, c, &msgs, line)
			if err != nil {
				fmt.Printf("  %s\n", helpers.ErrorStyle.Render(err.Error()))
			}
			if done {
				return nil
			}
			continue
		}

		msgs = append(msgs, message{Role: "user", Content: line})
		fmt.Printf("\n%s ", helpers.SubtitleStyle.Render("ai ▸"))
		answer, err := c.stream(ctx, msgs, os.Stdout)
		fmt.Println()
		if err != nil {
			fmt.Printf("  %s\n\n", helpers.ErrorStyle.Render(err.Error()))
			// Drop the turn that failed: leaving it in history would make every
			// later turn re-send a question the model never answered.
			msgs = msgs[:len(msgs)-1]
			continue
		}
		msgs = append(msgs, message{Role: "assistant", Content: answer})
		fmt.Println()
	}
}

// chatCommand handles the slash commands. Returns true when the session ends.
func chatCommand(cmd *cobra.Command, p *platform, c *gwClient, msgs *[]message, line string) (bool, error) {
	fields := strings.Fields(line)
	arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	switch fields[0] {
	case "/exit", "/quit":
		return true, nil
	case "/clear":
		*msgs = (*msgs)[:1]
		fmt.Println("  conversation cleared")
		return false, nil
	case "/model":
		if arg == "" {
			fmt.Printf("  model %s (needs a key in: %s)\n", c.model, providerForModel(c.model))
			return false, nil
		}
		c.model = arg
		fmt.Printf("  model switched to %s (needs a key in: %s)\n", c.model, providerForModel(c.model))
		return false, nil
	case "/context":
		(*msgs)[0] = message{Role: "system", Content: systemPrompt(ensureContext(cmd.Context()), p, true, nil)}
		fmt.Println("  cluster context refreshed")
		return false, nil
	case "/save":
		if arg == "" {
			return false, fmt.Errorf("/save needs a filename")
		}
		return false, saveTranscript(arg, *msgs)
	case "/help":
		fmt.Println("  /model /context /clear /save /exit")
		return false, nil
	}
	return false, fmt.Errorf("unknown command %s (try /help)", fields[0])
}

func saveTranscript(path string, msgs []message) error {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", m.Role, m.Content)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	fmt.Printf("  transcript written to %s\n", path)
	return nil
}
