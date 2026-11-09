package config

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

type ASRSettings struct {
	MaxUploadBytes        int64    `yaml:"max_upload_bytes,omitempty" json:"max_upload_bytes,omitempty"`
	MaxMessageBytes       int64    `yaml:"max_message_bytes,omitempty" json:"max_message_bytes,omitempty"`
	SessionTimeoutSeconds float64  `yaml:"session_timeout_seconds,omitempty" json:"session_timeout_seconds,omitempty"`
	AllowedOrigins        []string `yaml:"allowed_origins,omitempty" json:"allowed_origins,omitempty"`
}

func (s ASRSettings) UploadLimit() int64 {
	if s.MaxUploadBytes > 0 {
		return s.MaxUploadBytes
	}
	return 32 << 20
}
func (s ASRSettings) MessageLimit() int64 {
	if s.MaxMessageBytes > 0 {
		return s.MaxMessageBytes
	}
	return 1 << 20
}
func (s ASRSettings) SessionTimeout() time.Duration {
	if s.SessionTimeoutSeconds > 0 {
		return time.Duration(s.SessionTimeoutSeconds * float64(time.Second))
	}
	return 10 * time.Minute
}

func ValidateRealtimePath(path string) error {
	if path == "" {
		return nil
	}
	u, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("realtime_path must be an absolute path on the configured upstream, without a query or fragment")
	}
	return nil
}

func (s ASRSettings) Validate() error {
	if s.MaxUploadBytes < 0 || s.MaxMessageBytes < 0 || s.SessionTimeoutSeconds < 0 || math.IsNaN(s.SessionTimeoutSeconds) || math.IsInf(s.SessionTimeoutSeconds, 0) || s.SessionTimeoutSeconds > float64(math.MaxInt64)/float64(time.Second) {
		return fmt.Errorf("ASR limits must be finite nonnegative values within time.Duration range")
	}
	return nil
}
