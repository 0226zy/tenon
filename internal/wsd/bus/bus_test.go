package bus

import (
	"encoding/json"
	"testing"
)

func TestSlowSubscriberDropsOldest(t *testing.T) {
	b := New()
	messages, cancel := b.Subscribe()
	defer cancel()
	for i := 0; i < 65; i++ {
		b.Publish("progress", i)
	}
	first := <-messages
	var value int
	if err := json.Unmarshal(first.Data, &value); err != nil {
		t.Fatal(err)
	}
	if value != 1 {
		t.Fatalf("oldest queued value = %d, want 1", value)
	}
	b.Close()
	if _, ok := <-messages; ok {
		// Drain the bounded queue; Close must eventually close the channel.
		for range messages {
		}
	}
}
