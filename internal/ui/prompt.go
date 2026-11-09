package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
)

// Confirm asks for yes/no. Returns true immediately if unattended.
func Confirm(message string, unattended bool) (bool, error) {
	if unattended {
		return true, nil
	}
	var confirmed bool
	err := huh.NewConfirm().
		Title(message).
		Value(&confirmed).
		Run()
	return confirmed, err
}

// ConfirmDefault asks for yes/no with def pre-selected. Returns def
// immediately if unattended, so profile values survive both paths.
func ConfirmDefault(message string, def, unattended bool) (bool, error) {
	if unattended {
		return def, nil
	}
	confirmed := def
	err := huh.NewConfirm().
		Title(message).
		Value(&confirmed).
		Run()
	return confirmed, err
}

// Select presents a choice. Returns defaultVal if unattended.
func Select(message string, options []string, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	selected := defaultVal
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, o)
	}
	err := huh.NewSelect[string]().
		Title(message).
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		return defaultVal, err
	}
	return selected, nil
}

// Input asks for text input. Returns defaultVal if unattended.
func Input(message, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	var value string
	err := huh.NewInput().
		Title(message).
		Value(&value).
		Placeholder(defaultVal).
		Run()
	if err != nil {
		return defaultVal, err
	}
	if value == "" {
		return defaultVal, nil
	}
	return value, nil
}

// InputInt asks for an integer input. Returns defaultVal if unattended or empty.
func InputInt(message string, defaultVal int, unattended bool) (int, error) {
	s, err := Input(message, strconv.Itoa(defaultVal), unattended)
	if err != nil {
		return defaultVal, err
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return defaultVal, fmt.Errorf("invalid number %q: %w", s, err)
	}
	return v, nil
}

// InputIntSlice asks for a comma-separated list of integers.
func InputIntSlice(message string, defaultVal []int, unattended bool) ([]int, error) {
	parts := make([]string, len(defaultVal))
	for i, v := range defaultVal {
		parts[i] = strconv.Itoa(v)
	}
	s, err := Input(message, strings.Join(parts, ","), unattended)
	if err != nil {
		return defaultVal, err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultVal, nil
	}
	tokens := strings.Split(s, ",")
	result := make([]int, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		v, err := strconv.Atoi(t)
		if err != nil {
			return defaultVal, fmt.Errorf("invalid number %q: %w", t, err)
		}
		result = append(result, v)
	}
	return result, nil
}
