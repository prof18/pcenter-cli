package cli

import (
	"bufio"
	"fmt"
	"os"

	"golang.org/x/term"
)

// promptLine reads one visible line from stdin.
func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read input: %w", err)
	}
	return line, nil
}

// promptSecret reads a line without echoing it, so a client secret does not end
// up in a screen share or a scrollback buffer.
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return string(value), nil
}
