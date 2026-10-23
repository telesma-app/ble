package ble

import (
	"testing"
	"time"
)

func TestValueQueuePreservesOrder(t *testing.T) {
	queue := newValueQueue()
	for _, value := range [][]byte{{1}, {2}, {3}} {
		if !queue.Send(value) {
			t.Fatal("Send returned false")
		}
	}

	for want := byte(1); want <= 3; want++ {
		select {
		case got := <-queue.Listen():
			if len(got) != 1 || got[0] != want {
				t.Fatalf("value = %v, want [%d]", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued value")
		}
	}

	queue.Close()

	if queue.Send([]byte{4}) {
		t.Fatal("Send succeeded after Close")
	}

	if _, ok := <-queue.Listen(); ok {
		t.Fatal("Listen remained open after Close")
	}
}

func TestValueQueueCloseUnblocksUnreadDelivery(t *testing.T) {
	queue := newValueQueue()
	queue.Send([]byte{1})

	done := make(chan struct{})

	go func() {
		queue.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on unread value")
	}
}
