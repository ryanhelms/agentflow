package manifest

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

const (
	maxSettingKey   = 40
	maxSettingLabel = 64
	maxSettingDesc  = 512
	maxEnumValues   = 64
	maxEnumBytes    = 64
	maxListItems    = 64
)

var settingKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// enabledLabel is the label of the toggle every plugin gets, so a setting
// cannot take it.
const enabledLabel = "Enabled"

// refusedWidgets are controls that need a secret or a path, or hold
// structured data: phase 5 gives plugins neither (ADR, decision 7).
var refusedWidgets = []config.Widget{
	config.WidgetCredentialRef, config.WidgetFilePicker, config.WidgetBranchPicker, config.WidgetProviderPicker,
	config.WidgetKVTable, config.WidgetList,
}

// DefaultWidget is the control of a setting that names none: toggle, select
// (with enum), text, stepper and chips by type.
func DefaultWidget(s Setting) config.Widget {
	switch s.Type {
	case SettingBoolean:
		return config.WidgetToggle
	case SettingInteger:
		return config.WidgetStepper
	case SettingStringList:
		return config.WidgetChips
	}
	if len(s.Enum) > 0 {
		return config.WidgetSelect
	}
	return config.WidgetText
}

// SettingWidget is the control of s: its own widget, or DefaultWidget.
func SettingWidget(s Setting) config.Widget {
	if s.Widget != "" {
		return s.Widget
	}
	return DefaultWidget(s)
}

// settingList checks every setting and returns the declared keys.
func (v *validator) settingList(list []Setting) map[string]bool {
	keys := map[string]bool{}
	if len(list) > MaxSettings {
		v.add("settings", CodeLimit, "a plugin has at most %d settings", MaxSettings)
	}
	labels := map[string]bool{strings.ToLower(enabledLabel): true}
	for i, s := range list {
		at := fmt.Sprintf("settings[%d]", i)
		if keys[s.Key] {
			v.add(at+".key", CodeSetting, "two settings are named %q", s.Key)
		}
		keys[s.Key] = true
		for _, p := range SettingProblems(s) {
			v.add(at+"."+p.Path, p.Code, "%s", p.Message)
		}
		if l := strings.ToLower(strings.TrimSpace(s.Label)); l != "" {
			if labels[l] {
				v.add(at+".label", CodeSetting, "the label %q is already taken in this plugin's settings group", s.Label)
			}
			labels[l] = true
		}
	}
	return keys
}

// SettingProblems checks one setting on its own. Each Problem's Path is
// relative to the setting ("key", "default"). CatalogFields leaves out a
// setting that has any.
func SettingProblems(s Setting) []Problem {
	var out []Problem
	add := func(path, code, format string, args ...any) {
		out = append(out, Problem{Path: path, Code: code, Message: fmt.Sprintf(format, args...)})
	}
	if len(s.Key) > maxSettingKey || !settingKeyRe.MatchString(s.Key) {
		add("key", CodeSetting, "key is lowercase letters, digits and underscores, starting with a letter, at most %d characters", maxSettingKey)
	}
	switch {
	case strings.TrimSpace(s.Label) == "":
		add("label", CodeRequired, "label is required")
	case len(s.Label) > maxSettingLabel || !plainText(s.Label, false):
		add("label", CodeSetting, "label is plain text on one line, at most %d bytes", maxSettingLabel)
	}
	switch {
	case strings.TrimSpace(s.Description) == "":
		add("description", CodeRequired, "description is required")
	case len(s.Description) > maxSettingDesc || !plainText(s.Description, true):
		add("description", CodeSetting, "description is plain text, at most %d bytes", maxSettingDesc)
	}
	switch s.Applies {
	case "", config.AppliesLive, config.AppliesRestart:
	default:
		add("applies", CodeSetting, "applies is live or restart, not %q", s.Applies)
	}
	if !slices.Contains([]SettingType{SettingBoolean, SettingString, SettingInteger, SettingStringList}, s.Type) {
		add("type", CodeSetting, "type is boolean, string, integer or string-list, not %q", s.Type)
		return out
	}
	out = append(out, widgetProblems(s)...)
	out = append(out, limitProblems(s)...)
	if p, ok := defaultProblem(s); !ok {
		out = append(out, Problem{Path: "default", Code: CodeSetting, Message: p})
	}
	return out
}

func widgetProblems(s Setting) []Problem {
	var out []Problem
	add := func(path, format string, args ...any) {
		out = append(out, Problem{Path: path, Code: CodeSetting, Message: fmt.Sprintf(format, args...)})
	}
	if len(s.Enum) > 0 && s.Type != SettingString {
		add("enum", "only a string setting has an enum")
	}
	if s.Type == SettingString && len(s.Enum) > 0 {
		seen := map[string]bool{}
		if len(s.Enum) > maxEnumValues {
			add("enum", "enum has more than %d values", maxEnumValues)
		}
		for _, e := range s.Enum {
			switch {
			case e == "" || len(e) > maxEnumBytes || !plainText(e, false):
				add("enum", "an enum value is plain text on one line, at most %d bytes", maxEnumBytes)
			case seen[e]:
				add("enum", "enum lists %q twice", e)
			}
			seen[e] = true
		}
	}
	w := SettingWidget(s)
	switch {
	case slices.Contains(refusedWidgets, w):
		add("widget", "widget %q is not available to plugins: no secret or path reaches a plugin", w)
	case !widgetSuits(w, s):
		add("widget", "widget %q does not suit a %s setting%s", w, s.Type, map[bool]string{true: " with an enum", false: ""}[len(s.Enum) > 0])
	}
	return out
}

func widgetSuits(w config.Widget, s Setting) bool {
	switch s.Type {
	case SettingBoolean:
		return w == config.WidgetToggle
	case SettingInteger:
		return w == config.WidgetStepper
	case SettingStringList:
		return w == config.WidgetChips
	}
	if len(s.Enum) > 0 {
		return w == config.WidgetSelect
	}
	return w == config.WidgetText || w == config.WidgetDuration
}

func limitProblems(s Setting) []Problem {
	var out []Problem
	add := func(path, format string, args ...any) {
		out = append(out, Problem{Path: path, Code: CodeSetting, Message: fmt.Sprintf(format, args...)})
	}
	if s.Type == SettingInteger {
		switch {
		case s.Min == nil:
			add("min", "an integer setting needs min: its control has a lower bound")
		case s.Max != nil && *s.Max < *s.Min:
			add("max", "max %d is below min %d", *s.Max, *s.Min)
		}
	} else if s.Min != nil || s.Max != nil {
		add("min", "only an integer setting has min and max")
	}
	if SettingWidget(s) != config.WidgetDuration {
		if s.MinDuration != "" || s.MaxDuration != "" {
			add("min_duration", "only a duration setting has min_duration and max_duration")
		}
		return out
	}
	lo, loErr := parseDuration(s.MinDuration)
	hi, hiErr := parseDuration(s.MaxDuration)
	switch {
	case loErr != nil:
		add("min_duration", "min_duration is not a duration such as 30s or 5m")
	case hiErr != nil:
		add("max_duration", "max_duration is not a duration such as 30s or 5m")
	case s.MinDuration != "" && s.MaxDuration != "" && lo > hi:
		add("max_duration", "max_duration is shorter than min_duration")
	}
	return out
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err == nil && d <= 0 {
		err = fmt.Errorf("not positive")
	}
	return d, err
}

// defaultProblem checks the default against the setting's type and limits.
// ok is false with the reason when it does not fit.
func defaultProblem(s Setting) (string, bool) {
	if s.Default == nil {
		return "", true
	}
	d, ok := SettingDefault(s)
	if !ok {
		return fmt.Sprintf("default does not fit a %s setting", s.Type), false
	}
	switch s.Type {
	case SettingString:
		str := d.(string)
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			return "default is not one of enum", false
		}
		if SettingWidget(s) == config.WidgetDuration {
			dur, err := parseDuration(str)
			lo, _ := parseDuration(s.MinDuration)
			hi, _ := parseDuration(s.MaxDuration)
			if err != nil || str == "" || dur < lo || hi > 0 && dur > hi {
				return "default is not a duration inside min_duration and max_duration", false
			}
		}
	case SettingInteger:
		n := d.(int)
		if s.Min != nil && n < *s.Min || s.Max != nil && n > *s.Max {
			return "default is outside min and max", false
		}
	}
	return "", true
}

// SettingDefault returns the default of s as its schema type: bool, string,
// int or []string. ok is false when the manifest's default does not fit the
// type. A setting with no default returns nil, true. A value that went
// through the registry's JSON (a float64) is accepted when it is whole.
func SettingDefault(s Setting) (any, bool) {
	if s.Default == nil {
		return nil, true
	}
	switch s.Type {
	case SettingBoolean:
		b, ok := s.Default.(bool)
		return b, ok
	case SettingString:
		str, ok := s.Default.(string)
		return str, ok && plainText(str, false) && len(str) <= maxSettingDesc
	case SettingInteger:
		return wholeNumber(s.Default)
	case SettingStringList:
		var list []string
		switch t := s.Default.(type) {
		case []string:
			list = t
		case []any:
			for _, e := range t {
				str, ok := e.(string)
				if !ok {
					return nil, false
				}
				list = append(list, str)
			}
		default:
			return nil, false
		}
		if len(list) > maxListItems {
			return nil, false
		}
		for _, e := range list {
			if !plainText(e, false) || len(e) > maxEnumBytes {
				return nil, false
			}
		}
		if list == nil {
			list = []string{}
		}
		return list, true
	}
	return nil, false
}

func wholeNumber(x any) (int, bool) {
	switch n := x.(type) {
	case int:
		return n, true
	case int64:
		return int(n), n == int64(int(n))
	case uint64:
		if n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case float64:
		return int(n), n == math.Trunc(n) && math.Abs(n) < 1<<31
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil && i == int64(int(i))
	}
	return 0, false
}
