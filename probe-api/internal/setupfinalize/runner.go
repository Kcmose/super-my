package setupfinalize

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(context.Context, string, ...string) error
	RunQuiet(context.Context, string, ...string) error
	RunSensitive(context.Context, []byte, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("command %s failed", filepathBase(name))
	}
	return nil
}

func (OSRunner) RunQuiet(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("command %s failed", filepathBase(name))
	}
	return nil
}

func (OSRunner) RunSensitive(ctx context.Context, stdin []byte, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("sensitive command %s failed without output", filepathBase(name))
	}
	return nil
}

func (OSRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	// Output commands are limited to listener inspection and local PostgreSQL
	// catalog checks; neither receives a password. Preserve their stderr in the
	// root-only systemd journal so an installation failure is diagnosable without
	// reflecting any submitted secret through the setup HTTP protocol.
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		clear(output)
		return nil, fmt.Errorf("command %s failed", filepathBase(name))
	}
	return output, nil
}

func filepathBase(value string) string {
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[index+1:]
	}
	return value
}

func wildcardListener(output []byte, port string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address := fields[3]
		if listenerPort(address) != port {
			continue
		}
		if strings.HasPrefix(address, "0.0.0.0:") || strings.HasPrefix(address, "[::]:") || strings.HasPrefix(address, "*:") {
			return true
		}
	}
	return false
}

func anyPortListener(output []byte, port string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && listenerPort(fields[3]) == port {
			return true
		}
	}
	return false
}

func listenerPort(address string) string {
	index := strings.LastIndexByte(address, ':')
	if index < 0 {
		return ""
	}
	return strings.TrimSuffix(address[index+1:], "]")
}
