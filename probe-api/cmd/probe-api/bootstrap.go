package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"probe-api/internal/auth"
)

func bootstrapAdministrator(
	ctx context.Context,
	service *auth.Service,
	username string,
	input *os.File,
	output io.Writer,
	prompt io.Writer,
) error {
	if service == nil || input == nil || output == nil || prompt == nil {
		return errors.New("administrator bootstrap is not configured")
	}
	inputFD := int(input.Fd())
	if !term.IsTerminal(inputFD) {
		return errors.New("administrator bootstrap requires an interactive terminal")
	}

	password, err := readHiddenPassword(inputFD, prompt, "New administrator password: ")
	if err != nil {
		return err
	}
	defer clear(password)
	confirmation, err := readHiddenPassword(inputFD, prompt, "Confirm administrator password: ")
	if err != nil {
		return err
	}
	matched := subtle.ConstantTimeCompare(password, confirmation) == 1
	clear(confirmation)
	if !matched {
		return errors.New("administrator passwords do not match")
	}

	requestID, err := newBootstrapRequestID()
	if err != nil {
		return err
	}
	user, err := service.BootstrapAdmin(ctx, username, password, requestID)
	if errors.Is(err, auth.ErrBootstrapUnavailable) {
		return errors.New("administrator bootstrap is unavailable because the database already contains a user")
	}
	if err != nil {
		return fmt.Errorf("bootstrap administrator: %w", err)
	}
	if _, err := fmt.Fprintf(output, "administrator %s created\n", user.Username); err != nil {
		return fmt.Errorf("administrator %s was created, but writing confirmation failed: %w", user.Username, err)
	}
	return nil
}

func readHiddenPassword(inputFD int, prompt io.Writer, message string) ([]byte, error) {
	if _, err := io.WriteString(prompt, message); err != nil {
		return nil, fmt.Errorf("write password prompt: %w", err)
	}
	value, err := term.ReadPassword(inputFD)
	if _, newlineErr := io.WriteString(prompt, "\n"); err == nil && newlineErr != nil {
		err = newlineErr
	}
	if err != nil {
		clear(value)
		return nil, errors.New("read administrator password from terminal")
	}
	return value, nil
}

func newBootstrapRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate administrator bootstrap request ID")
	}
	return "local-bootstrap-" + hex.EncodeToString(value), nil
}
