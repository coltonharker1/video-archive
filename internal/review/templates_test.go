package review

import (
	"bytes"
	"html/template"
	"testing"

	"github.com/colton/video-archive/internal/model"
)

// These tests don't assert output correctness — they just catch template
// parse errors and missing field references that would otherwise only
// surface on live render.

func TestTemplatesParse(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		data any
	}{
		{"home", homeTemplate, homePageData{}},
		{"identities", identitiesTemplate, identitiesPageData{}},
		{"identity_detail", identityDetailTemplate, identityDetailData{Identity: model.Identity{Name: "Test"}}},
		{"review", reviewTemplate, reviewData{NavHTML: template.HTML("")}},
		{"groups", groupsTemplate, groupsPageData{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			funcs := template.FuncMap{
				"formatDuration": func(ms int64) string { return "" },
				"isSelected":     func(id, selected int64) bool { return false },
			}
			tmpl, err := template.New(tc.name).Funcs(funcs).Parse(tc.tmpl)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.name, err)
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, tc.data); err != nil {
				t.Fatalf("execute %s: %v", tc.name, err)
			}
		})
	}
}
