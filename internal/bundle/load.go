package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"envinit/internal/spec"
)

func Load(path string) (spec.Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return spec.Bundle{}, fmt.Errorf("read bundle file: %w", err)
	}

	var bundle spec.Bundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return spec.Bundle{}, fmt.Errorf("parse bundle json: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return spec.Bundle{}, err
	}
	applyDetectedPlatformDefaults(&bundle, defaultPlatformDetector())
	bundle.ApplyDefaults()
	if err := bundle.Validate(); err != nil {
		return spec.Bundle{}, err
	}
	return bundle, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("parse bundle json: %w", err)
	}
	return fmt.Errorf("parse bundle json: multiple JSON values are not allowed")
}
