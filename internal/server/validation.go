package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxNameRunes              = 200
	maxRefRunes               = 200
	maxShortTextRunes         = 500
	maxFreeTextRunes          = 20_000
	maxGenericQueryKeyRunes   = 64
	maxGenericQueryValueRunes = 2_000
	maxGenericOrderingRunes   = 120
	maxGenericBodyBytes       = 64 << 10
	maxGenericDepth           = 16
	maxGenericArrayItems      = 500
	maxGenericObjectKeys      = 200
	maxGenericBodyStringRunes = 8_000
	maxGenericResultBytes     = 256 << 10
)

var orderingPattern = regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_]*$`)

func cleanName(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	return validateBoundedString(label, s, maxNameRunes, false)
}

func cleanOptionalName(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	return validateBoundedString(label, s, maxNameRunes, false)
}

func cleanRef(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	return validateBoundedString(label, s, maxRefRunes, false)
}

func cleanOptionalShortText(label, s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	return validateBoundedString(label, s, maxShortTextRunes, false)
}

func cleanOptionalFreeText(label, s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	return validateBoundedString(label, s, maxFreeTextRunes, true)
}

func validateBoundedString(label, s string, maxRunes int, allowTextWhitespace bool) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s must be valid UTF-8", label)
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return "", fmt.Errorf("%s is too long: max %d characters", label, maxRunes)
	}
	for _, r := range s {
		if r == 0 {
			return "", fmt.Errorf("%s must not contain NUL bytes", label)
		}
		if r < 0x20 {
			if allowTextWhitespace && (r == '\n' || r == '\r' || r == '\t') {
				continue
			}
			return "", fmt.Errorf("%s contains unsupported control characters", label)
		}
	}
	return s, nil
}

func validatePositiveIDString(label, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%s must be a positive integer id", label)
		}
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("%s must be a positive integer id", label)
	}
	return raw, nil
}

func validatePositiveID(label string, id int) error {
	if id <= 0 {
		return fmt.Errorf("%s must be a positive integer id", label)
	}
	return nil
}

func validateGenericQueryValue(label, s string) error {
	_, err := validateBoundedString(label, s, maxGenericQueryValueRunes, false)
	return err
}

func validateGenericQueryKey(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if _, err := validateBoundedString(label, s, maxGenericQueryKeyRunes, false); err != nil {
		return "", err
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("%s may contain only letters, numbers, underscore, and dash", label)
	}
	return s, nil
}

func validateOrdering(s string) error {
	if s == "" {
		return nil
	}
	if _, err := validateBoundedString("ordering", s, maxGenericOrderingRunes, false); err != nil {
		return err
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || !orderingPattern.MatchString(part) {
			return fmt.Errorf("ordering contains unsupported field %q", part)
		}
	}
	return nil
}

func validateGenericBody(body map[string]any) error {
	if len(body) == 0 {
		return nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding generic body: %w", err)
	}
	if len(b) > maxGenericBodyBytes {
		return fmt.Errorf("generic body is too large: max %d bytes", maxGenericBodyBytes)
	}
	return validateGenericValue("body", body, 0)
}

func validateGenericValue(label string, v any, depth int) error {
	if depth > maxGenericDepth {
		return fmt.Errorf("%s is nested too deeply: max depth %d", label, maxGenericDepth)
	}
	switch x := v.(type) {
	case nil, bool, float64, int, int64, json.Number:
		return nil
	case string:
		_, err := validateBoundedString(label, x, maxGenericBodyStringRunes, true)
		return err
	case []any:
		if len(x) > maxGenericArrayItems {
			return fmt.Errorf("%s has too many array items: max %d", label, maxGenericArrayItems)
		}
		for i, item := range x {
			if err := validateGenericValue(fmt.Sprintf("%s[%d]", label, i), item, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		if len(x) > maxGenericObjectKeys {
			return fmt.Errorf("%s has too many object keys: max %d", label, maxGenericObjectKeys)
		}
		for k, item := range x {
			if _, err := validateGenericQueryKey(label+" key", k); err != nil {
				return fmt.Errorf("%s key %q is invalid: %w", label, k, err)
			}
			if err := validateGenericValue(label+"."+k, item, depth+1); err != nil {
				return err
			}
		}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Errorf("%s contains unsupported value type %T", label, v)
		}
		var normalized any
		if err := json.Unmarshal(b, &normalized); err != nil {
			return fmt.Errorf("%s contains unsupported JSON value: %w", label, err)
		}
		return validateGenericValue(label, normalized, depth+1)
	}
	return nil
}

func validatePublicHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("a URL is required")
	}
	if _, err := validateBoundedString("URL", raw, maxGenericQueryValueRunes, false); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("URL must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("URL must not include credentials")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("URL must not include a fragment")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf("URL must include a host")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return "", fmt.Errorf("URL has invalid port %q", port)
		}
	}
	if strings.Contains(host, "%") {
		return "", fmt.Errorf("URL host must not contain zone identifiers")
	}
	if ip := net.ParseIP(host); ip != nil && isUnsafeFetchIP(ip) {
		return "", fmt.Errorf("URL host %q is not allowed for server-side fetching", host)
	}
	if isUnsafeFetchHost(host) {
		return "", fmt.Errorf("URL host %q is not allowed for server-side fetching", host)
	}
	return raw, nil
}

func isUnsafeFetchIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
