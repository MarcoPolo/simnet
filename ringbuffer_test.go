package simnet

import "testing"

func TestRingBufferPushPopOrder(t *testing.T) {
	rb := newRingBuffer[int](2)
	total := 10

	for i := range total {
		rb.PushBack(i)
	}

	for i := range total {
		got := rb.PopFront()
		if got != i {
			t.Fatalf("PopFront()=%d, want %d", got, i)
		}
	}
}

func TestRingBufferWrapAndGrowth(t *testing.T) {
	rb := newRingBuffer[int](4)

	for _, v := range []int{0, 1, 2} {
		rb.PushBack(v)
	}

	if got := rb.PopFront(); got != 0 {
		t.Fatalf("PopFront()=%d, want 0", got)
	}

	for _, v := range []int{3, 4, 5} {
		rb.PushBack(v)
	}

	want := []int{1, 2, 3, 4, 5}
	for _, v := range want {
		got := rb.PopFront()
		if got != v {
			t.Fatalf("PopFront()=%d, want %d", got, v)
		}
	}
}

func TestRingBufferPeekAndEmpty(t *testing.T) {
	rb := newRingBuffer[string](1)

	if !rb.Empty() {
		t.Fatalf("empty()=false, want true")
	}

	rb.PushBack("hello")

	if rb.Empty() {
		t.Fatalf("empty()=true, want false")
	}

	if got := rb.Peek(); got != "hello" {
		t.Fatalf("Peek()=%q, want %q", got, "hello")
	}

	if got := rb.PopFront(); got != "hello" {
		t.Fatalf("PopFront()=%q, want %q", got, "hello")
	}

	if !rb.Empty() {
		t.Fatalf("empty()=false, want true after PopFront")
	}
}
