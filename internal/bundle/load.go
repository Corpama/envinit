package bundle

import (
	"encoding/json"
	"fmt"
	"os"

	"envinit/internal/spec"
)

func Load(path string) (spec.Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return spec.Bundle{}, fmt.Errorf("read bundle file: %w", err)
	}

	var bundle spec.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return spec.Bundle{}, fmt.Errorf("parse bundle json: %w", err)
	}
	applyDetectedPlatformDefaults(&bundle, defaultPlatformDetector())
	bundle.ApplyDefaults()
	if err := bundle.Validate(); err != nil {
		return spec.Bundle{}, err
	}
	return bundle, nil
}
