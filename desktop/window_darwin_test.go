//go:build darwin

package main

import "testing"

func TestPageZoomFor(t *testing.T) {
	cases := []struct {
		name          string
		width, height float64
		want          float64
	}{
		{"design size", designWidth, designHeight, 1},
		{"larger window never zooms in", 2560, 1440, 1},
		{"narrow window shrinks by width", 1050, 900, 0.75},
		{"short window shrinks by height", 1400, 720, 0.8},
		{"tighter axis wins", 1024, 680, 1024.0 / designWidth},
		{"floor", 400, 300, minPageZoom},
	}
	for _, tc := range cases {
		if got := pageZoomFor(tc.width, tc.height); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Errorf("%s: pageZoomFor(%v, %v) = %v, want %v", tc.name, tc.width, tc.height, got, tc.want)
		}
	}
}

// The minimum window must still fit the whole layout at the zoom floor.
func TestMinimumWindowFitsTheLayoutAtTheFloor(t *testing.T) {
	if z := pageZoomFor(float64(minWidth), float64(minHeight)); float64(minWidth)/z < designWidth-1 || float64(minHeight)/z < designHeight-1 {
		t.Fatalf("minimum window %dx%d at zoom %v shows only %vx%v of the %dx%d layout",
			minWidth, minHeight, z, float64(minWidth)/z, float64(minHeight)/z, designWidth, designHeight)
	}
}
