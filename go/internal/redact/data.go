package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Data scrubs diagnostic JSON while keeping measured quantities, nulls,
// timestamps and coverage. Config's numeric allowlist is for settings and
// would drop sample counts and other evidence from these reads.
func (r *Redactor) Data(doc any) any { return r.data(doc, "") }

var dataPath = regexp.MustCompile(`(?:[A-Za-z]:\\|/)[^\s"'<>]*[/\\][^\s"'<>]*`)
var dataMetric = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func dataSource(value string) bool {
	switch value {
	case "hardware_counter", "power_integrated", "power_integration", "mixed", "unknown", "ntp", "rtc", "system", "manual", "estimated", "vehicle", "charger", "telemetry", "forecast":
		return true
	}
	return false
}

func dataKey(key string) string {
	var b strings.Builder
	letters := []rune(key)
	for i, c := range letters {
		if unicode.IsUpper(c) && i > 0 && (unicode.IsLower(letters[i-1]) || unicode.IsDigit(letters[i-1]) || i+1 < len(letters) && unicode.IsLower(letters[i+1])) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

func dataID(key string) bool {
	return key == "id" || strings.HasSuffix(key, "_id") || key == "src" || key == "driver" || key == "drivers" ||
		key == "name" || key == "names" || key == "label" || key == "title" || key == "alias" || strings.HasSuffix(key, "_name")
}

func (r *Redactor) data(doc any, key string) any {
	switch v := doc.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, value := range v {
			field := dataKey(k)
			if secretKey(field) || removedKey(field) || field == "vin" || field == "imei" || field == "serial_number" || field == "path" || field == "file" || field == "directory" || field == "home_id" || field == "box_id" {
				continue
			}
			name := k
			if r.names != nil && r.names.MatchString(k) || reURL.MatchString(k) || reIPv4.MatchString(k) || reEmail.MatchString(k) || reMAC.MatchString(k) || reHexRun.MatchString(k) || reHost.MatchString(k) || strings.Count(k, ":") >= 3 && reIPv6.MatchString(k) || strings.Contains(k, "::") {
				name = r.ID(k)
			}
			out[name] = r.data(value, field)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = r.data(value, key)
		}
		return out
	case string:
		if v == "" {
			return v
		}
		// Metric names are queryable schema, not free-text device values.
		// The log scrubber would turn names such as pv_mppt1_w into [id].
		if (key == "metric" || key == "metrics") && dataMetric.MatchString(v) {
			return v
		}
		if dataID(key) || key == "source" && !dataSource(v) {
			return r.ID(v)
		}
		if _, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return v
		}
		// Recorded planner inputs can themselves be JSON strings.
		if strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[") {
			var nested any
			dec := json.NewDecoder(bytes.NewBufferString(v))
			dec.UseNumber()
			if dec.Decode(&nested) == nil && json.Valid([]byte(v)) {
				body, err := json.Marshal(r.data(nested, ""))
				if err == nil {
					return string(body)
				}
			}
		}
		return dataPath.ReplaceAllString(r.Text(v), "[path]")
	default:
		if dataID(key) && isNumber(doc) {
			return r.ID(fmt.Sprint(doc))
		}
		return doc
	}
}
