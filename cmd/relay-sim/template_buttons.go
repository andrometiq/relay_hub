package main

import (
	"fmt"
	"strings"
)

// buttonLimits are Meta's per-type maximums in one template.
var buttonLimits = map[string]int{"QUICK_REPLY": 10, "URL": 2, "PHONE_NUMBER": 1, "COPY_CODE": 1, "OTP": 1, "FLOW": 1}

// templateButtonProblem mirrors the Meta message_templates button rules, so the simulator
// refuses what Meta refuses: at most 10 buttons, per-type limits, quick replies kept in one
// group and a copy code button with a single string example and no text.
func templateButtonProblem(components any) string {
	rows, _ := components.([]any)
	for _, row := range rows {
		component, _ := row.(map[string]any)
		if !strings.EqualFold(fmt.Sprint(component["type"]), "BUTTONS") {
			continue
		}
		buttons, _ := component["buttons"].([]any)
		if len(buttons) > 10 {
			return "Templates can have at most 10 buttons."
		}
		counts, groups, previous := map[string]int{}, 0, ""
		for _, item := range buttons {
			button, _ := item.(map[string]any)
			kind := strings.ToUpper(fmt.Sprint(button["type"]))
			counts[kind]++
			if limit, ok := buttonLimits[kind]; ok && counts[kind] > limit {
				return fmt.Sprintf("Templates can have at most %d %s buttons.", limit, kind)
			}
			if group := fmt.Sprint(kind == "QUICK_REPLY"); group != previous {
				groups, previous = groups+1, group
			}
			if kind == "COPY_CODE" {
				if _, ok := button["example"].(string); !ok || button["text"] != nil {
					return "A COPY_CODE button takes one string example and no text."
				}
			}
		}
		if groups > 2 {
			return "Quick reply buttons must be grouped together, before or after the other buttons."
		}
	}
	return ""
}

func templateRejection(problem string) map[string]any {
	return map[string]any{"error": map[string]any{"code": 100, "message": "Invalid parameter", "error_user_title": "Invalid buttons", "error_user_msg": problem}}
}
