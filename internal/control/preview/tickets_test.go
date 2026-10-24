package preview

import (
	"testing"
	"time"
)

func TestTicketSingleUseAndHostBinding(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	tickets := NewTickets(master, time.Minute)
	host := "abcdefghijklmnopqrstuvwxyz.preview.localhost:8081"
	ticket, err := tickets.Issue("abcdefghij12", host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ticket, "other.preview.localhost:8081"); err == nil {
		t.Fatal("ticket was accepted on another host")
	}
	convID, err := tickets.Consume(ticket, host)
	if err != nil || convID != "abcdefghij12" {
		t.Fatalf("consume conv=%q err=%v", convID, err)
	}
	if _, err := tickets.Consume(ticket, host); err == nil {
		t.Fatal("ticket replay succeeded")
	}
}

func TestCookieHostAndSignatureBinding(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	cookie := signCookie(master, "abcdefghij12", "one.preview.localhost:8081", time.Minute)
	if _, err := verifyCookie(master, cookie, "two.preview.localhost:8081"); err == nil {
		t.Fatal("cookie was accepted on another host")
	}
	convID, err := verifyCookie(master, cookie, "one.preview.localhost:8081")
	if err != nil || convID != "abcdefghij12" {
		t.Fatalf("cookie conv=%q err=%v", convID, err)
	}
	if _, err := verifyCookie(master, cookie+"x", "one.preview.localhost:8081"); err == nil {
		t.Fatal("tampered cookie was accepted")
	}
}
