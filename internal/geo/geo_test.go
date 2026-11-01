package geo

import (
	"math"
	"testing"
)

func TestHaversine(t *testing.T) {
	if d := Haversine(10, 20, 10, 20); d != 0 {
		t.Errorf("same point = %v", d)
	}
	if d := Haversine(0, 0, 0, 1); math.Abs(d-111195) > 100 {
		t.Errorf("one degree of longitude at the equator = %v", d)
	}
	// Tel Aviv to Jerusalem is roughly 54 km.
	if d := Haversine(32.0853, 34.7818, 31.7683, 35.2137); d < 52000 || d > 56000 {
		t.Errorf("Tel Aviv-Jerusalem = %v", d)
	}
	if a, b := Haversine(1, 2, 3, 4), Haversine(3, 4, 1, 2); a != b {
		t.Errorf("not symmetric: %v %v", a, b)
	}
	if d := Haversine(90, 0, -90, 0); math.IsNaN(d) || math.Abs(d-math.Pi*earthRadiusM) > 1 {
		t.Errorf("pole to pole = %v", d)
	}
}
