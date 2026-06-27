package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// OptionsFromEnv reads server-layer options from environment variables.
func OptionsFromEnv() (Options, error) {
	opts := Options{
		ImageDir:         os.Getenv("TANDOOR_IMAGE_DIR"),
		OperationTimeout: defaultOperationTimeout,
	}
	if v := strings.TrimSpace(os.Getenv("TANDOOR_OPERATION_TIMEOUT")); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			return opts, fmt.Errorf("TANDOOR_OPERATION_TIMEOUT must be a positive number of seconds, got %q", v)
		}
		opts.OperationTimeout = time.Duration(secs) * time.Second
	}
	return opts, nil
}
