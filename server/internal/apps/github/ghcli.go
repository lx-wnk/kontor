package github

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ghTokenTimeout bounds the credential lookup. It reads a local credential
// store, so it is either fast or broken.
var ghTokenTimeout = 5 * time.Second

// ErrGhNotFound reports that the GitHub CLI is not on PATH.
var ErrGhNotFound = errors.New("the GitHub CLI (gh) is not on PATH")

// TokenFromGhCLI returns the token the GitHub CLI is authenticated with, so the
// dashboard can reach GitHub without a personal access token stored in its own
// database. The token stays in gh's credential store and is read once per
// start; nothing about it is persisted here.
//
// The command is hardcoded. No setting, request or environment value reaches
// its argument list -- the point of this path is to hold one fewer secret, not
// to gain a way to run a configured command.
//
// What it does NOT buy: an agent with Bash runs as the same OS user and can
// invoke `gh` itself, so this narrows what is *stored*, not what an agent on
// this machine can *reach*.
func TokenFromGhCLI(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTokenTimeout)
	defer cancel()

	// #nosec G204 -- every argument is a constant in this file; nothing from a
	// setting, a request or the environment reaches the command line, and
	// exec.CommandContext passes argv without a shell.
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// gh's own stderr says what is wrong and how to fix it, far better
			// than anything this package could phrase ("To get started with
			// GitHub CLI, please run: gh auth login").
			detail := strings.TrimSpace(string(exitErr.Stderr))
			if detail == "" {
				detail = err.Error()
			}
			return "", fmt.Errorf("gh auth token failed: %s", detail)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrGhNotFound
		}
		return "", fmt.Errorf("gh auth token: %w", err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh auth token returned nothing; run `gh auth login`")
	}
	return token, nil
}
