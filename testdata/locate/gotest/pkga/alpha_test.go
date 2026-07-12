package pkga

import "testing"

// TestOne is at line 5.
func TestOne(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math broke")
	}
}

// TestTwo is at line 12.
func TestTwo(t *testing.T) {
	_ = t
}

// BenchmarkThing is at line 17.
func BenchmarkThing(b *testing.B) {
	for i := 0; i < b.N; i++ {
	}
}
