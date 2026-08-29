package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"

	"github.com/justenwalker/kevin/plugin"
)

//go:embed config_schema.cue
var configSchema []byte

// icon is a small flat-color square, generated once at package init rather
// than checked in as a binary asset - a demo of Plugin.Icon, not branding.
var icon = demoIcon()

func demoIcon() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 0x4a, G: 0x9e, B: 0xd6, A: 0xff}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// config is the decoded with block of one step.
type config struct {
	Message         string            `json:"message"`
	Delay           string            `json:"delay"`
	Fail            bool              `json:"fail"`
	Outputs         map[string]string `json:"outputs"`
	Details         []detailConfig    `json:"details"`
	Export          map[string]string `json:"export"`
	ExportSensitive []string          `json:"export_sensitive"`
}

// detailConfig is one entry of an echo step's with-block "details" list -
// it maps straight onto plugin.Detail, so a test (or a person) can put
// arbitrary rows on the step's console card.
type detailConfig struct {
	Label    string `json:"label"`
	Value    string `json:"value"`
	Copyable bool   `json:"copyable"`
	Href     string `json:"href"`
}

func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("echo: decode config: %w", err)
	}
	return cfg, nil
}

// providerConfig is the decoded config block of the echo plugin itself.
type providerConfig struct {
	Greeting string `json:"greeting"`
}

// greeting holds the value that Configure receives, for an echo step to log.
var greeting struct {
	mu    sync.RWMutex
	value string
}

// configure decodes the config block of the provider and stores the
// greeting. Every echo step logs the greeting when it runs.
func configure(_ context.Context, data []byte, _ plugin.Env) error {
	var cfg providerConfig
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("echo: decode provider config: %w", err)
		}
	}
	greeting.mu.Lock()
	greeting.value = cfg.Greeting
	greeting.mu.Unlock()
	return nil
}

// currentGreeting returns the greeting that Configure last received.
func currentGreeting() string {
	greeting.mu.RLock()
	defer greeting.mu.RUnlock()
	return greeting.value
}
