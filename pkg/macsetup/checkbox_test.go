package macsetup

import (
	"encoding/json"
	"image"
	_ "image/jpeg"
	"os"
	"testing"
)

func TestNativeSiriCheckboxAndInterruptedResume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		checked bool
	}{{"sequoia", true}, {"tahoe", true}, {"tahoe-unchecked", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open("testdata/" + tc.name + "-siri.jpg")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			frame, _, err := image.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("testdata/" + tc.name + "-siri.json")
			if err != nil {
				t.Fatal(err)
			}
			var s Screen
			if err = json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			s.Checks = CheckboxStates(frame, s)
			if checked, known := s.Checks["Enable Ask Siri"]; !known || checked != tc.checked {
				t.Fatalf("checkbox not observed: %v bounds=%v", s.Checks, frame.Bounds())
			}
			plan, err := Next(s, DefaultConfig())
			want := "siri-disabled"
			if tc.checked {
				want = "siri-disable"
			}
			if err != nil || plan.Stage != want || len(plan.Actions) != 1 {
				t.Fatalf("%+v %v", plan, err)
			}
			// An interrupted run resumes from an already unchecked checkbox without
			// re-enabling Siri, and advances through the separate Continue control.
			s.Checks["Enable Ask Siri"] = false
			plan, err = Next(s, DefaultConfig())
			if err != nil || plan.Stage != "siri-disabled" || len(plan.Actions) != 1 {
				t.Fatalf("%+v %v", plan, err)
			}
			s.Checks = CheckboxStates(image.NewRGBA(frame.Bounds()), Screen{})
			if len(s.Checks) != 0 {
				t.Fatal("invented an unobserved control")
			}
		})
	}
}
