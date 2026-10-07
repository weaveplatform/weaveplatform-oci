package macsetup

import "image"

// CheckboxStates observes the standard colored checkboxes beside known setup
// labels. Unknown controls remain absent so retries cannot toggle them blindly.
func CheckboxStates(frame image.Image, screen Screen) map[string]bool {
	states := make(map[string]bool)
	for _, label := range []string{"Enable Ask Siri"} {
		matches := screen.exact(label)
		if len(matches) != 1 {
			continue
		}
		t := matches[0]
		area := image.Rect(t.X-2*t.Height, t.Y-t.Height/2, t.X-t.Height/3, t.Y+3*t.Height/2).
			Intersect(frame.Bounds())
		colored, outline := 0, 0
		for y := area.Min.Y; y < area.Max.Y; y++ {
			for x := area.Min.X; x < area.Max.X; x++ {
				red, green, blue, _ := frame.At(x, y).RGBA()
				r, g, b := int(red>>8), int(green>>8), int(blue>>8)
				if max(r, g, b)-min(r, g, b) > 60 {
					colored++
				}
				// Tahoe uses a pale grey fill for unchecked controls.
				if max(r, g, b) < 245 {
					outline++
				}
			}
		}
		if colored > 2*t.Height {
			states[label] = true
		} else if outline > 2*t.Height {
			states[label] = false
		}
	}
	return states
}
